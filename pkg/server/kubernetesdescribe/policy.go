package kubernetesdescribe

import (
	"context"
	"sort"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"k8s.io/apimachinery/pkg/api/resource"
)

type PolicyInput struct {
	ClusterID string
	Namespace string
	Resource  kubernetesresource.PolicyResource
	Name      string
}

// DescribePolicy supports the two policy resources whose status answers an
// operator's concrete question: which quota stopped admission, or why an
// eviction is blocked. Resources without status are rejected by the HTTP
// boundary rather than returning an authoritative-looking empty diagnosis.
func (service *Service) DescribePolicy(
	ctx context.Context,
	input PolicyInput,
) (Result, error) {
	if service == nil || service.resources == nil {
		return Result{}, ErrInvalidInput
	}
	if input.Resource != kubernetesresource.PolicyResourceQuotas &&
		input.Resource != kubernetesresource.PolicyDisruptionBudgets {
		return Result{}, ErrInvalidInput
	}
	policy, err := service.resources.GetPolicyResource(
		ctx, input.ClusterID, input.Namespace, input.Resource, input.Name,
	)
	if err != nil {
		return Result{}, err
	}
	if policy.UID == "" {
		return Result{}, kubernetesresource.ErrInvalidResponse
	}
	status := PolicyStatus{QuotaUsage: policyQuotaUsage(policy)}
	result := Result{
		Target: Target{
			APIVersion: policy.APIVersion, Kind: policy.Kind,
			Namespace: policy.Namespace, Name: policy.Name, UID: policy.UID,
			ResourceVersion: policy.ResourceVersion,
		},
		Family: FamilyPolicy, Policy: &policy, PolicyStatus: &status,
		Findings: policyFindings(policy, status), DegradedSections: []string{},
	}
	result.Events, _ = service.objectEvents(ctx, input.ClusterID, result.Target)
	if result.Events.Omitted == EventsOmittedUnavailable {
		result.DegradedSections = append(result.DegradedSections, "events")
	}
	return result, nil
}

func policyQuotaUsage(policy kubernetesresource.PolicyResourceDetail) []PolicyQuotaUsage {
	if policy.ResourceQuota == nil {
		return []PolicyQuotaUsage{}
	}
	keys := make([]string, 0, len(policy.ResourceQuota.Hard))
	for name := range policy.ResourceQuota.Hard {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	usage := make([]PolicyQuotaUsage, 0, len(keys))
	for _, name := range keys {
		hard := policy.ResourceQuota.Hard[name]
		used := policy.ResourceQuota.Used[name]
		if used == "" {
			used = "0"
		}
		hardQuantity, hardErr := resource.ParseQuantity(hard)
		usedQuantity, usedErr := resource.ParseQuantity(used)
		usage = append(usage, PolicyQuotaUsage{
			Resource: name, Used: used, Hard: hard,
			Exhausted: hardErr == nil && usedErr == nil && usedQuantity.Cmp(hardQuantity) >= 0,
		})
	}
	return usage
}
