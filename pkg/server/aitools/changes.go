package aitools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

const (
	defaultChangeMinutes = 120
	maxChangeMinutes     = 7 * 24 * 60
	defaultChangeLimit   = 50
	maxChangeLimit       = pagination.MaxLimit
	defaultVerifyMinutes = 15
	minimumObservation   = time.Minute
)

// Direct mutations recorded by the ordinary Server paths. DryRun actions are
// deliberately absent: a preview is evidence about a proposed change, not a
// possible cause of the state the operator is investigating.
var clusterChangeActions = []string{
	auditaction.ClusterUpdate,
	auditaction.ClusterSuspend,
	auditaction.ClusterResume,
	auditaction.ClusterDelete,
	auditaction.ClusterConnectionRevoke,
	auditaction.ClusterConnectionReenroll,
	auditaction.ClusterMetricsCollectorInstall,
	auditaction.ClusterMetricsCollectorUninstall,
	auditaction.KubernetesResourceCreate,
	auditaction.KubernetesResourceUpdate,
	auditaction.KubernetesResourcePatch,
	auditaction.KubernetesResourceDelete,
	auditaction.KubernetesPodEvict,
	auditaction.KubernetesCronJobTrigger,
	auditaction.KubernetesNodeDrain,
	auditaction.KubernetesHelmReleaseInstall,
	auditaction.KubernetesHelmReleaseUpgrade,
	auditaction.KubernetesHelmReleaseRollback,
	auditaction.KubernetesHelmReleaseUninstall,
}

type listClusterChangesArguments struct {
	Minutes       int  `json:"minutes"`
	Limit         int  `json:"limit"`
	IncludeFailed bool `json:"include_failed"`
}

type changeEntry struct {
	ID         string            `json:"id"`
	OccurredAt string            `json:"occurred_at"`
	ActorType  string            `json:"actor_type"`
	Actor      string            `json:"actor"`
	Action     string            `json:"action"`
	Tool       string            `json:"tool,omitempty"`
	TargetType string            `json:"target_type"`
	Target     string            `json:"target"`
	Result     string            `json:"result"`
	RequestID  string            `json:"request_id"`
	Detail     map[string]string `json:"detail,omitempty"`
	createdAt  time.Time
}

func (catalogue *Catalogue) listClusterChanges(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments listClusterChangesArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	minutes := bound(arguments.Minutes, defaultChangeMinutes, maxChangeMinutes)
	limit := bound(arguments.Limit, defaultChangeLimit, maxChangeLimit)
	now := time.Now().UTC()
	since := now.Add(-time.Duration(minutes) * time.Minute)
	resultFilter := "succeeded"
	if arguments.IncludeFailed {
		resultFilter = ""
	}

	direct, err := catalogue.dependencies.Changes.Query(ctx, audit.QueryInput{
		UserID: invocation.UserID, ClusterID: invocation.ClusterID,
		Result: resultFilter, Actions: clusterChangeActions, Since: since,
		Page: pagination.Request{Limit: maxChangeLimit},
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	// AIOps calls the same services below the HTTP handlers, so their deployment
	// audit row is ai_tool.invoke rather than a duplicate generic mutation row.
	// The runtime records the stable mutating marker and resolved target on that
	// row; filtering it in SQL keeps the many read calls out of this timeline.
	aiChanges, err := catalogue.dependencies.Changes.Query(ctx, audit.QueryInput{
		UserID: invocation.UserID, ClusterID: invocation.ClusterID,
		Result: resultFilter, Actions: []string{auditaction.AIToolInvoke}, Since: since,
		DetailContains: map[string]string{"mutating": "true"},
		Page:           pagination.Request{Limit: maxChangeLimit},
	})
	if err != nil {
		return airuntime.ToolResult{}, err
	}

	entries := make([]changeEntry, 0, len(direct.Events)+len(aiChanges.Events))
	for _, event := range direct.Events {
		entries = append(entries, changeFromAudit(event))
	}
	for _, event := range aiChanges.Events {
		entries = append(entries, changeFromAudit(event))
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].createdAt.After(entries[j].createdAt)
	})
	total := direct.Page.Total + aiChanges.Page.Total
	if len(entries) > limit {
		entries = entries[:limit]
	}
	for index := range entries {
		entries[index].createdAt = time.Time{}
	}
	return airuntime.ToolResult{Text: catalogue.encode(map[string]any{
		"cluster_id": invocation.ClusterID,
		"from":       since.Format(time.RFC3339),
		"to":         now.Format(time.RFC3339),
		"total":      total,
		"returned":   len(entries),
		"has_more":   total > len(entries),
		"changes":    entries,
	})}, nil
}

func changeFromAudit(event audit.Event) changeEntry {
	actor := event.ActorUserName
	if actor == "" {
		actor = event.ActorUserID
	}
	if actor == "" {
		actor = event.ActorAgentID
	}
	target := event.TargetName
	tool := ""
	if event.Action == auditaction.AIToolInvoke {
		tool = event.Detail["tool"]
		if name := event.Detail["resource_name"]; name != "" {
			target = strings.TrimSpace(event.Detail["gvk"] + " " +
				objectPath(event.Detail["namespace"], name))
		}
	}
	return changeEntry{
		ID: event.ID, OccurredAt: event.CreatedAt.UTC().Format(time.RFC3339),
		ActorType: event.ActorType, Actor: actor, Action: event.Action, Tool: tool,
		TargetType: event.TargetType, Target: target, Result: event.Result,
		RequestID: event.RequestID, Detail: event.Detail, createdAt: event.CreatedAt,
	}
}

func objectPath(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}

type verifyResourceChangeArguments struct {
	APIVersion string `json:"api_version"`
	Kind       string `json:"kind"`
	Namespace  string `json:"namespace"`
	Name       string `json:"name"`
	ChangedAt  string `json:"changed_at"`
}

func verifyResourceChangeTarget(arguments json.RawMessage) *aisession.Target {
	var request verifyResourceChangeArguments
	if decode(arguments, &request) != nil {
		return nil
	}
	return &aisession.Target{
		Namespace: strings.TrimSpace(request.Namespace),
		GVK:       groupVersionKind(request.APIVersion, request.Kind),
		Name:      strings.TrimSpace(request.Name),
	}
}

func (catalogue *Catalogue) verifyResourceChange(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments verifyResourceChangeArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	now := time.Now().UTC()
	changedAt := now.Add(-defaultVerifyMinutes * time.Minute)
	if strings.TrimSpace(arguments.ChangedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(arguments.ChangedAt))
		if err != nil {
			return airuntime.ToolResult{}, fmt.Errorf("%w: changed_at 必须是 RFC3339 时间", airuntime.ErrInvalidInput)
		}
		changedAt = parsed.UTC()
	}
	if changedAt.After(now.Add(time.Minute)) || changedAt.Before(now.Add(-7*24*time.Hour)) {
		return airuntime.ToolResult{}, fmt.Errorf("%w: changed_at 必须位于最近 7 天且不能在未来", airuntime.ErrInvalidInput)
	}

	identity, resource, err := catalogue.resolve(
		ctx, invocation.ClusterID, arguments.APIVersion, arguments.Kind,
	)
	if err != nil {
		return airuntime.ToolResult{}, err
	}
	var described kubernetesdescribe.Result
	if resource.Kind == "Pod" && identity.Group == "" {
		described, err = catalogue.dependencies.Describe.DescribePod(ctx, kubernetesdescribe.PodInput{
			ClusterID: invocation.ClusterID, Namespace: arguments.Namespace, Name: arguments.Name,
		})
	} else {
		described, err = catalogue.dependencies.Describe.DescribeResource(ctx, kubernetesdescribe.ResourceInput{
			ClusterID: invocation.ClusterID, Resource: identity,
			Namespace: arguments.Namespace, Name: arguments.Name,
		})
	}
	if err != nil {
		return airuntime.ToolResult{}, err
	}

	digest, recentEvents := changeVerificationDigest(described, changedAt, now)
	gvk := groupVersionKind(described.Target.APIVersion, described.Target.Kind)
	evidence := []aisession.Evidence{{
		Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID,
		Namespace: described.Target.Namespace, GVK: gvk, Name: described.Target.Name,
		ResourceVersion: described.Target.ResourceVersion,
	}}
	if recentEvents > 0 {
		evidence = append(evidence, aisession.Evidence{
			Kind: aisession.EvidenceEvent, Cluster: invocation.ClusterID,
			Namespace: described.Target.Namespace, GVK: gvk, Name: described.Target.Name,
		})
	}
	return airuntime.ToolResult{
		Text: catalogue.encode(digest), Evidence: evidence,
		Target: &aisession.Target{
			Namespace: described.Target.Namespace, GVK: gvk, Name: described.Target.Name,
		},
	}, nil
}

func changeVerificationDigest(
	result kubernetesdescribe.Result, changedAt, checkedAt time.Time,
) (map[string]any, int) {
	reasons := make([]string, 0, 8)
	warnings := make([]map[string]any, 0, len(result.Events.Items))
	recentEvents, unknownTime := 0, 0
	for _, event := range result.Events.Items {
		seen := event.LastSeen
		if seen == nil {
			seen = event.FirstSeen
		}
		if seen == nil {
			unknownTime++
			continue
		}
		if seen.Before(changedAt) {
			continue
		}
		recentEvents++
		if !strings.EqualFold(event.Type, "Warning") {
			continue
		}
		warnings = append(warnings, map[string]any{
			"time": seen.UTC().Format(time.RFC3339), "reason": event.Reason,
			"message": event.Message, "object": event.Regarding.Kind + "/" + event.Regarding.Name,
		})
	}
	if len(result.Findings) > 0 {
		reasons = append(reasons, "current_findings")
	}
	if len(warnings) > 0 {
		reasons = append(reasons, "warning_events_after_change")
	}

	rollout := map[string]any{}
	if workload := result.Workload; workload != nil {
		rollout["generation"] = workload.Generation
		rollout["observed_generation"] = workload.ObservedGeneration
		rollout["status"] = workload.Status
		if workload.Replicas != nil {
			rollout["replicas"] = workload.Replicas
		}
		if workload.ObservedGeneration < workload.Generation {
			reasons = append(reasons, "generation_not_observed")
		}
		if replicas := workload.Replicas; replicas != nil &&
			(replicas.Ready < replicas.Desired || replicas.Available < replicas.Desired ||
				replicas.Unavailable > 0) {
			reasons = append(reasons, "replicas_not_converged")
		}
	}
	if pod := result.Pod; pod != nil && !pod.Ready && pod.Phase != "Succeeded" {
		reasons = append(reasons, "pod_not_ready")
	}
	unreadyRelated := 0
	relatedTruncated := false
	if result.Related != nil {
		for _, item := range result.Related.Pods {
			if len(item.Findings) > 0 || (continuousWorkload(result.Workload) && !item.Ready) {
				unreadyRelated++
			}
		}
		if unreadyRelated > 0 {
			reasons = append(reasons, "related_objects_not_ready")
		}
		relatedTruncated = result.Related.Truncated
	}

	status := "passed"
	incomplete := result.Family == kubernetesdescribe.FamilyGeneric ||
		len(result.DegradedSections) > 0 || result.Events.Omitted != "" ||
		result.Events.Truncated || relatedTruncated || unknownTime > 0 ||
		checkedAt.Sub(changedAt) < minimumObservation
	if len(reasons) > 0 {
		status = "warning"
	} else if incomplete {
		status = "inconclusive"
	}
	if result.Family == kubernetesdescribe.FamilyGeneric {
		reasons = append(reasons, "generic_resource_has_no_health_rules")
	}
	if len(result.DegradedSections) > 0 || result.Events.Omitted != "" {
		reasons = append(reasons, "verification_sections_unavailable")
	}
	if result.Events.Truncated {
		reasons = append(reasons, "event_window_truncated")
	}
	if relatedTruncated {
		reasons = append(reasons, "related_objects_truncated")
	}
	if unknownTime > 0 {
		reasons = append(reasons, "event_time_unknown")
	}
	if checkedAt.Sub(changedAt) < minimumObservation {
		reasons = append(reasons, "observation_window_too_short")
	}

	digest := map[string]any{
		"status": status, "target": result.Target, "family": result.Family,
		"changed_at":          changedAt.Format(time.RFC3339),
		"checked_at":          checkedAt.Format(time.RFC3339),
		"observation_seconds": int64(checkedAt.Sub(changedAt).Seconds()),
		"reasons":             reasons, "findings": result.Findings,
		"warning_events_after_change": warnings,
		"events_after_change":         recentEvents,
		"events_with_unknown_time":    unknownTime,
		"degraded_sections":           result.DegradedSections,
		"events_truncated":            result.Events.Truncated,
		"events_omitted":              result.Events.Omitted,
		"unready_related_objects":     unreadyRelated,
	}
	if len(rollout) > 0 {
		digest["rollout"] = rollout
	}
	return digest, recentEvents
}

func continuousWorkload(workload *kubernetesresource.WorkloadDetail) bool {
	if workload == nil {
		return false
	}
	switch workload.Kind {
	case "Deployment", "StatefulSet", "DaemonSet":
		return true
	default:
		return false
	}
}
