package platformsettings

import (
	"fmt"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/observability"
)

// WorkloadSettings is what an operator owns about one installed workload. It is
// the store's type rather than a copy of it: this package would otherwise carry
// a six-field struct and a copy loop that exist only to restate the shape.
type WorkloadSettings = store.WorkloadSettings

// The workloads ZKE installs into a Cluster.
//
// The three metrics names are the observability component names by
// construction. The Agent reports install state under those names and the
// Console already shows them, so a second spelling for the same workload would
// leave the platform with two vocabularies for one thing.
const (
	WorkloadAgent            = "agent"
	WorkloadClusterTerminal  = "cluster-terminal"
	WorkloadMetricsCollector = observability.ComponentCollector
	WorkloadKubeStateMetrics = observability.ComponentKubeState
	WorkloadNodeExporter     = observability.ComponentNodeExporter
)

// workloadLabels names each workload in a rejection the operator reads. The
// form holds five images and twenty quantities, and a refusal that does not say
// which one it means is a refusal they have to bisect.
//
// Which workloads exist is the Server's to declare rather than the database's,
// because the Server is the only side that knows what reads them. Every one of
// them is installed with a resource budget, so there is nothing else to declare
// about them here.
var workloadLabels = map[string]string{
	WorkloadAgent:            "Agent",
	WorkloadClusterTerminal:  "Cluster Terminal",
	WorkloadMetricsCollector: "指标采集组件",
	WorkloadKubeStateMetrics: "kube-state-metrics",
	WorkloadNodeExporter:     "node-exporter",
}

// requireDeclaredWorkloads refuses a settings set that is missing a workload the
// Server declares.
//
// This is what stands in for a database constraint on the component name. A
// migration that inserts a misspelled name, or a workload added to this registry
// without one, would otherwise install an empty image into somebody else's
// Cluster; failing the read instead makes it a startup-visible error.
func requireDeclaredWorkloads(settings store.PlatformSettings) error {
	for component := range workloadLabels {
		if _, declared := settings.Workloads[component]; !declared {
			return fmt.Errorf("platform settings are missing workload %q", component)
		}
	}
	return nil
}
