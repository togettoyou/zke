package aiskills

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/aitools"
	"github.com/togettoyou/zke/pkg/server/clusteroverview"
	"github.com/togettoyou/zke/pkg/server/clusterterminal"
	"github.com/togettoyou/zke/pkg/server/helm"
	"github.com/togettoyou/zke/pkg/server/kubernetesdescribe"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
	"github.com/togettoyou/zke/pkg/server/podlogs"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
	"github.com/togettoyou/zke/pkg/shared/kubernetescatalog"
)

// A skill naming a tool that does not exist is not a loud failure: the runtime
// drops it, and the deployment quietly ships one playbook fewer than it thinks.
// Holding the library against the fully composed catalogue is what turns a tool
// rename into a failing test instead of a silent regression.
func TestEveryShippedSkillNamesToolsTheCatalogueHas(t *testing.T) {
	t.Parallel()
	available := specNames(aitools.New(fullDependencies(), aitools.Config{}).Specs())
	for _, skill := range New().Skills() {
		for _, tool := range skill.Tools {
			if !slices.Contains(available, tool) {
				t.Errorf("skill %q names tool %q, which the catalogue does not have", skill.ID, tool)
			}
		}
	}
}

// The catalogue is a list somebody reads under pressure. Two skills sharing an
// id would make one of them unreachable, and a summary that is a paragraph
// would make the index in the system prompt cost more than the skills save.
func TestShippedSkillsAreWellFormed(t *testing.T) {
	t.Parallel()
	const maxSummaryRunes = 60
	seen := make(map[string]struct{})
	for _, skill := range New().Skills() {
		if _, duplicate := seen[skill.ID]; duplicate {
			t.Errorf("duplicate skill id %q", skill.ID)
		}
		seen[skill.ID] = struct{}{}
		if skill.ID == "" || skill.Title == "" || skill.Summary == "" || skill.Body == "" {
			t.Errorf("skill %q has an empty field", skill.ID)
		}
		if len(skill.Tools) == 0 {
			t.Errorf("skill %q names no tools, so nothing decides whether it is usable", skill.ID)
		}
		if strings.ContainsAny(skill.Summary, "\r\n") {
			t.Errorf("skill %q has a multi-line summary", skill.ID)
		}
		if length := len([]rune(skill.Summary)); length > maxSummaryRunes {
			t.Errorf("skill %q summary is %d runes, above %d", skill.ID, length, maxSummaryRunes)
		}
	}
}

// A playbook may only direct the model at tools that read. Naming a write here
// would let a procedure carry a change past the point where an operator decides
// to make one, which is the whole reason skills hold no authority of their own.
func TestNoShippedSkillDirectsAWrite(t *testing.T) {
	t.Parallel()
	specs := aitools.New(fullDependencies(), aitools.Config{}).Specs()
	for _, skill := range New().Skills() {
		for _, tool := range skill.Tools {
			for _, spec := range specs {
				if spec.Name == tool && spec.Mutating {
					t.Errorf("skill %q names mutating tool %q", skill.ID, tool)
				}
			}
		}
	}
}

func specNames(specs []airuntime.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	return names
}

// fullDependencies composes every optional dependency so the catalogue is the
// widest one a deployment can have. Nothing is called: the test is about which
// tools exist, not about what they return.
func fullDependencies() aitools.Dependencies {
	stub := unusedDependency{}
	return aitools.Dependencies{
		Resources: stub, Overview: stub, Describe: stub, Logs: stub, Metrics: stub,
		Workloads: stub, Revisions: stub, Scopes: stub, Manifests: stub,
		Terminal: stub, Permissions: stub, Helm: stub, HelmWrites: stub,
		Charts: stub, GlobalPermissions: stub,
		ManifestAccess: func(kubernetesresource.ManifestGrant) kubernetesmanifest.ResourceAccess {
			panic("not called")
		},
	}
}

type unusedDependency struct{}

func (unusedDependency) ListHelmReleases(
	context.Context, kubernetesresource.ListHelmReleasesInput,
) (kubernetesresource.HelmReleasePage, error) {
	panic("not called")
}

func (unusedDependency) ListHelmReleaseRevisions(
	context.Context, string, string, string,
) (kubernetesresource.HelmReleasePage, error) {
	panic("not called")
}

func (unusedDependency) GetHelmRelease(
	context.Context, string, string, string, int64,
) (kubernetesresource.HelmReleaseDetail, error) {
	panic("not called")
}

func (unusedDependency) AuthorizeGlobal(context.Context, string, rbac.Permission) error {
	panic("not called")
}

func (unusedDependency) ListRepositories(context.Context) (helm.RepositoryPage, error) {
	panic("not called")
}

func (unusedDependency) ListCharts(
	context.Context, string, string, int,
) (helm.ChartPage, error) {
	panic("not called")
}

func (unusedDependency) ListChartVersions(
	context.Context, string, string,
) (helm.ChartVersionPage, error) {
	panic("not called")
}

func (unusedDependency) GetChart(
	context.Context, string, string, string,
) (helm.ChartDetail, error) {
	panic("not called")
}

func (unusedDependency) Install(
	context.Context, helm.InstallInput,
) (helmrelease.Report, error) {
	panic("not called")
}

func (unusedDependency) Upgrade(
	context.Context, helm.UpgradeInput,
) (helmrelease.Report, error) {
	panic("not called")
}

func (unusedDependency) Rollback(
	context.Context, helm.RollbackInput,
) (helmrelease.Report, error) {
	panic("not called")
}

func (unusedDependency) Uninstall(
	context.Context, helm.UninstallInput,
) (helmrelease.Report, error) {
	panic("not called")
}

func (unusedDependency) DiscoverResources(context.Context, string) (kubernetescatalog.Catalog, error) {
	panic("not called")
}

func (unusedDependency) ListResources(
	context.Context, kubernetesresource.ListResourcesInput,
) (kubernetesresource.ResourcePage, error) {
	panic("not called")
}

func (unusedDependency) GetResource(
	context.Context, kubernetesresource.GetResourceInput,
) (map[string]any, error) {
	panic("not called")
}

func (unusedDependency) ListNodes(
	context.Context, kubernetesresource.ListNodesInput,
) (kubernetesresource.NodePage, error) {
	panic("not called")
}

func (unusedDependency) Get(context.Context, string) (clusteroverview.Overview, error) {
	panic("not called")
}

func (unusedDependency) DescribePod(
	context.Context, kubernetesdescribe.PodInput,
) (kubernetesdescribe.Result, error) {
	panic("not called")
}

func (unusedDependency) DescribeResource(
	context.Context, kubernetesdescribe.ResourceInput,
) (kubernetesdescribe.Result, error) {
	panic("not called")
}

func (unusedDependency) Stream(
	context.Context, podlogs.Input, io.Writer,
) (podlogs.Result, error) {
	panic("not called")
}

func (unusedDependency) Catalog() []metricsquery.Definition { panic("not called") }

func (unusedDependency) Query(
	context.Context, metricsquery.Input,
) (metricsquery.Result, error) {
	panic("not called")
}

func (unusedDependency) ScaleWorkload(
	context.Context, kubernetesresource.ScaleWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	panic("not called")
}

func (unusedDependency) ListWorkloadRevisions(
	context.Context, kubernetesresource.ListWorkloadRevisionsInput,
) (kubernetesresource.WorkloadRevisionPage, error) {
	panic("not called")
}

func (unusedDependency) RollbackWorkload(
	context.Context, kubernetesresource.RollbackWorkloadInput,
) (kubernetesresource.WorkloadDetail, error) {
	panic("not called")
}

func (unusedDependency) ResolveClusterScope(
	context.Context, string,
) (rbac.ResolvedScope, error) {
	panic("not called")
}

func (unusedDependency) AuthorizeResolvedCluster(
	context.Context, string, rbac.Permission, rbac.ResolvedScope,
) error {
	panic("not called")
}

func (unusedDependency) AuthorizeCluster(
	context.Context, string, rbac.Permission, string,
) (rbac.ResolvedScope, error) {
	panic("not called")
}

func (unusedDependency) Execute(
	context.Context, kubernetesmanifest.ResourceAccess, kubernetesmanifest.Input,
) (kubernetesmanifest.Result, error) {
	panic("not called")
}

func (unusedDependency) CreateCommandSession(
	context.Context, clusterterminal.CommandSessionInput,
) (clusterterminal.CommandSession, error) {
	panic("not called")
}

func (unusedDependency) ExecuteCommand(
	context.Context, clusterterminal.CommandInput,
) (clusterterminal.CommandResult, error) {
	panic("not called")
}

func (unusedDependency) FinishCommandSession(
	context.Context, clusterterminal.CommandSession,
) error {
	panic("not called")
}
