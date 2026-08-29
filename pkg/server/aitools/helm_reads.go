package aitools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aisession"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// Helm releases, for the model.
//
// A release is the one thing in the container service that no other tool here
// can answer for. `list_resources` shows the Deployment a chart produced and
// the Deployment carries no reference back to the release that produced it, so
// "which application is installed here, at which chart version, and was it
// changed recently" has no answer in the rest of this catalogue — and a rollout
// that broke two hours ago is very often a Helm upgrade that happened two hours
// ago.
//
// Helm stores a release as a Secret per revision, which decides both halves of
// how these tools behave.
//
// The permission half: reading a release is reading a Secret, so every tool
// here requires `cluster.secret.read` on top of `cluster.read` — exactly what
// the Console's own release routes require. The same data through a different
// door must not answer to a lesser permission.
//
// The content half: the Secret's payload holds the values the chart was
// installed with, its rendered manifest and its NOTES.txt, and all three
// routinely carry a credential. AIOps does not put Secret content into the
// model context or the durable trail — the same invariant that makes the
// manifest tools refuse a Secret document and keeps Secret permissions out of
// the terminal's projected identity. So these tools return what a release *is*
// and never what it holds: chart identity, revision, status, timestamps, the
// *paths* the values overrode, and the identities of the objects the chart
// rendered. Values, notes and manifest bodies stay where an operator can open
// them under their own session, in the Helm application.
//
// Paths without values is a rule rather than a heuristic on purpose. A filter
// that tried to keep the harmless values and drop the dangerous ones would be
// guessing which of `image.tag` and `auth.rootPassword` a chart considers
// secret, and it would be wrong about somebody's chart.

const (
	// What one release digest may name. Both bounds are about the model
	// context rather than about safety: a chart with a thousand value paths is
	// answered by the Helm application, not by a tool result.
	maxHelmValuePaths      = 200
	maxHelmManifestObjects = 200
	// How many releases one listing cites. Same reasoning as the resource
	// listing: the first few are the ones a reader follows.
	maxHelmEvidence = 8
)

type helmReleaseListArguments struct {
	Namespace string `json:"namespace"`
}

type helmReleaseRevisionsArguments struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type helmReleaseArguments struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Revision  int64  `json:"revision"`
}

// helmReleaseTarget is what the approval prompt, the trajectory and the audit
// row name before the call runs. A release has no GVK of its own — it is the
// Secret family it is stored in — so the target carries the Namespace and the
// release name and leaves the Kind out rather than inventing one.
func helmReleaseTarget(arguments json.RawMessage) *aisession.Target {
	var input helmReleaseArguments
	if decode(arguments, &input) != nil {
		return nil
	}
	return &aisession.Target{Namespace: input.Namespace, Name: input.Name}
}

func (catalogue *Catalogue) listHelmReleases(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmReleaseListArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	page, err := catalogue.dependencies.Helm.ListHelmReleases(
		ctx, kubernetesresource.ListHelmReleasesInput{
			ClusterID: invocation.ClusterID, Namespace: arguments.Namespace,
		})
	if err != nil {
		if result, handled := helmReleaseFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	rows := make([]map[string]any, 0, len(page.Releases))
	evidence := make([]aisession.Evidence, 0, maxHelmEvidence)
	for _, release := range page.Releases {
		rows = append(rows, helmReleaseRow(release))
		if len(evidence) < maxHelmEvidence {
			evidence = append(evidence, helmReleaseEvidence(invocation.ClusterID, release))
		}
	}
	header := fmt.Sprintf(
		"Namespace %s 中有 %d 个 Helm Release（每行是该 Release 当前保留的最新 revision）：",
		arguments.Namespace, len(rows),
	)
	return airuntime.ToolResult{
		Text:     header + "\n" + catalogue.encode(rows),
		Evidence: evidence,
		Target:   &aisession.Target{Namespace: arguments.Namespace},
	}, nil
}

func (catalogue *Catalogue) listHelmReleaseRevisions(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmReleaseRevisionsArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	page, err := catalogue.dependencies.Helm.ListHelmReleaseRevisions(
		ctx, invocation.ClusterID, arguments.Namespace, arguments.Name,
	)
	if err != nil {
		if result, handled := helmReleaseFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	rows := make([]map[string]any, 0, len(page.Releases))
	for index, release := range page.Releases {
		row := helmReleaseRow(release)
		// Newest first is what the service returns, so the first row is the
		// revision the cluster is currently running. Saying so is the whole
		// point of the listing: a rollback target has to be one of the others.
		row["current"] = index == 0
		rows = append(rows, row)
	}
	header := fmt.Sprintf(
		"Release %s/%s 保留了 %d 个 revision（按版本号从新到旧；被 --history-max 清理掉的版本已经不在存储里）：",
		arguments.Namespace, arguments.Name, len(rows),
	)
	return airuntime.ToolResult{
		Text: header + "\n" + catalogue.encode(rows),
		Evidence: []aisession.Evidence{{
			Kind: aisession.EvidenceHelmRelease, Cluster: invocation.ClusterID,
			Namespace: arguments.Namespace, Name: arguments.Name,
		}},
		Target: &aisession.Target{Namespace: arguments.Namespace, Name: arguments.Name},
	}, nil
}

func (catalogue *Catalogue) getHelmRelease(
	ctx context.Context, invocation airuntime.ToolInvocation,
) (airuntime.ToolResult, error) {
	var arguments helmReleaseArguments
	if err := decode(invocation.Arguments, &arguments); err != nil {
		return airuntime.ToolResult{}, err
	}
	if arguments.Revision < 0 {
		return airuntime.ToolResult{}, fmt.Errorf(
			"%w: revision 不能为负数；省略它表示存储中保留的最新版本",
			airuntime.ErrInvalidInput)
	}
	detail, err := catalogue.dependencies.Helm.GetHelmRelease(
		ctx, invocation.ClusterID, arguments.Namespace, arguments.Name, arguments.Revision,
	)
	if err != nil {
		if result, handled := helmReleaseFailure(err); handled {
			return result, nil
		}
		return airuntime.ToolResult{}, err
	}
	digest, objects := helmReleaseDigest(detail)
	evidence := []aisession.Evidence{
		helmReleaseEvidence(invocation.ClusterID, detail.HelmRelease),
	}
	// The rendered objects are ordinary Kubernetes objects and answer to
	// `cluster.read` like any other, so they are cited as resources: following
	// one lands on the object itself, which is where a broken rollout is
	// actually diagnosed.
	for _, object := range objects {
		if len(evidence) >= maxHelmEvidence {
			break
		}
		evidence = append(evidence, aisession.Evidence{
			Kind: aisession.EvidenceResource, Cluster: invocation.ClusterID,
			Namespace: object.namespace,
			GVK:       groupVersionKind(object.apiVersion, object.kind),
			Name:      object.name,
		})
	}
	return airuntime.ToolResult{
		Text:     catalogue.encode(digest),
		Evidence: evidence,
		Target: &aisession.Target{
			Namespace: detail.Namespace, Name: detail.Name,
		},
	}, nil
}

// helmReleaseRow is what the Secret's labels say, which is all a listing reads.
func helmReleaseRow(release kubernetesresource.HelmRelease) map[string]any {
	return map[string]any{
		"namespace": release.Namespace,
		"name":      release.Name,
		"revision":  release.Revision,
		"status":    release.Status,
		"updated":   release.Updated.UTC().Format(time.RFC3339),
	}
}

func helmReleaseEvidence(
	clusterID string, release kubernetesresource.HelmRelease,
) aisession.Evidence {
	return aisession.Evidence{
		Kind: aisession.EvidenceHelmRelease, Cluster: clusterID,
		Namespace: release.Namespace, Name: release.Name,
	}
}

type helmRenderedObject struct {
	apiVersion string
	kind       string
	namespace  string
	name       string
}

// helmReleaseDigest is the release without its Secret content.
//
// It returns the rendered objects separately as well, because they are also
// the evidence the answer cites and rebuilding them from the digest's strings
// would be parsing a display format.
func helmReleaseDigest(
	detail kubernetesresource.HelmReleaseDetail,
) (map[string]any, []helmRenderedObject) {
	paths, pathsTruncated := helmValuePaths(detail.Values)
	objects, objectsPartial := helmManifestObjects(detail.Manifest)
	rendered := make([]string, 0, len(objects))
	for _, object := range objects {
		// Written the way kubectl names an object rather than as the slashed
		// GVK the evidence carries: this line is read by a person as often as
		// by the model, and `apps/v1/Deployment web/shop` reads as three path
		// segments rather than as a kind and an object.
		if object.namespace != "" {
			rendered = append(rendered, fmt.Sprintf(
				"%s %s %s/%s", object.apiVersion, object.kind, object.namespace, object.name))
			continue
		}
		rendered = append(rendered, fmt.Sprintf(
			"%s %s %s", object.apiVersion, object.kind, object.name))
	}
	digest := map[string]any{
		"namespace":         detail.Namespace,
		"name":              detail.Name,
		"revision":          detail.Revision,
		"status":            detail.Status,
		"description":       detail.Description,
		"chart_name":        detail.ChartName,
		"chart_version":     detail.ChartVersion,
		"app_version":       detail.AppVersion,
		"chart_description": detail.ChartDescription,
		"updated":           detail.Updated.UTC().Format(time.RFC3339),
		// Which values were overridden, never what they were set to. See the
		// package comment above: a release's values are Secret content.
		"overridden_value_paths":           paths,
		"overridden_value_paths_truncated": pathsTruncated,
		"rendered_objects":                 rendered,
		// Partial covers both halves of why the inventory can be short: the
		// service cuts a very long manifest before this ever sees it, and the
		// decode stops at whatever the cut left behind or at this file's own
		// object bound.
		"rendered_objects_partial": objectsPartial || detail.ManifestTruncated,
		"omitted": "values、NOTES.txt 与渲染后的 Manifest 正文属于 Secret 内容，不会进入 AIOps 上下文；" +
			"需要查看时请在 ZKE 的 Helm 应用或容器服务的 Helm 分区中打开。",
	}
	if detail.FirstDeployed != nil {
		digest["first_deployed"] = detail.FirstDeployed.UTC().Format(time.RFC3339)
	}
	if detail.LastDeployed != nil {
		digest["last_deployed"] = detail.LastDeployed.UTC().Format(time.RFC3339)
	}
	return digest, objects
}

// helmValuePaths lists the leaves of the values document.
//
// A slice is a leaf here even when it holds maps: descending into one produces
// a path per element that says nothing a reader can act on, and the elements
// are exactly where a list of credentials would be.
func helmValuePaths(values map[string]any) ([]string, bool) {
	paths := make([]string, 0, 32)
	var walk func(prefix string, node map[string]any)
	walk = func(prefix string, node map[string]any) {
		for key, value := range node {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if child, ok := value.(map[string]any); ok && len(child) > 0 {
				walk(path, child)
				continue
			}
			if _, ok := value.([]any); ok {
				path += "[]"
			}
			paths = append(paths, path)
		}
	}
	walk("", values)
	sort.Strings(paths)
	if len(paths) > maxHelmValuePaths {
		return paths[:maxHelmValuePaths], true
	}
	return paths, false
}

// The identity fields of one rendered document. Nothing else is unmarshalled:
// the rest of the document is the chart's output, which is what this tool is
// deliberately not returning.
type helmManifestIdentity struct {
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	Metadata   struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
}

// helmManifestObjects names what the chart rendered.
//
// Tolerant rather than strict: the manifest arrives already cut at the
// service's size bound, so the last document is routinely half a document. A
// strict multi-document decode would refuse the whole inventory over that one
// tail. Decoding stops at the first document that does not parse and reports
// the inventory as partial, which is the honest answer — the objects before it
// really are in the release.
func helmManifestObjects(manifest string) ([]helmRenderedObject, bool) {
	if strings.TrimSpace(manifest) == "" {
		return nil, false
	}
	decoder := yaml.NewDecoder(bytes.NewReader([]byte(manifest)))
	objects := make([]helmRenderedObject, 0, 16)
	for {
		var identity helmManifestIdentity
		err := decoder.Decode(&identity)
		if errors.Is(err, io.EOF) {
			return objects, false
		}
		if err != nil {
			return objects, true
		}
		if identity.Kind == "" || identity.Metadata.Name == "" {
			continue
		}
		objects = append(objects, helmRenderedObject{
			apiVersion: identity.APIVersion,
			kind:       identity.Kind,
			namespace:  identity.Metadata.Namespace,
			name:       identity.Metadata.Name,
		})
		if len(objects) == maxHelmManifestObjects {
			return objects, true
		}
	}
}

// helmReleaseFailure turns the three answers that are about the release rather
// than about the Cluster into a result the model can act on.
//
// The runtime's generic failure text says "the Agent may be unreachable or the
// object may not exist", which is the wrong instruction for all three of these:
// a release that is not there should be looked for under another name, and a
// Namespace holding too many revisions to group is not something a retry fixes.
func helmReleaseFailure(err error) (airuntime.ToolResult, bool) {
	switch {
	case errors.Is(err, kubernetesresource.ErrHelmReleaseNotFound):
		return airuntime.ToolResult{
			Text: "目标 Namespace 中没有这个 Helm Release，或它的 revision 已经被 Helm 的历史上限清理。" +
				"可以先用 list_helm_releases 确认名称。",
			Failed: true,
		}, true
	case errors.Is(err, kubernetesresource.ErrHelmReleaseInventoryTruncated):
		return airuntime.ToolResult{
			Text: "该 Namespace 的 Helm Release 修订记录超过一次清点的安全上限，无法可靠判断哪个是当前版本。" +
				"请改用 ZKE 的 Helm 应用查看，或换一个 Namespace 继续排查。",
			Failed: true,
		}, true
	case errors.Is(err, kubernetesresource.ErrHelmReleaseUnreadable):
		return airuntime.ToolResult{
			Text: "这个 Release 的存储内容无法解码：它可能由另一种 Helm 存储驱动写入，或已被 Helm 之外的工具改写。" +
				"这不是权限问题，也不是集群故障。",
			Failed: true,
		}, true
	}
	return airuntime.ToolResult{}, false
}
