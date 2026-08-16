// Package observability holds the object names the metrics pipeline uses
// inside a Cluster.
//
// They are shared because two sides have to agree on them without talking: the
// Server renders the Agent's Service and RBAC from them, and the Agent builds
// the collector's remote write URL and object set from them. A second copy of
// "zke-agent-metrics-ingest" in either place would be a silent misconfiguration
// rather than a build failure.
package observability

const (
	// IngestSecretName holds the credential the in-cluster collector presents.
	// It lives in the Agent Namespace and is created by the Agent itself, so
	// the value never leaves the Cluster.
	IngestSecretName = "zke-agent-metrics-ingest"
	// IngestTokenKey and IngestPreviousTokenKey let a rotation apply the new
	// value before every collector Pod has picked it up.
	IngestTokenKey         = "token"
	IngestPreviousTokenKey = "previous-token"

	// IngestServiceName is the ClusterIP Service in front of the Agent's
	// collector-facing endpoint. It is reachable inside the Cluster only.
	IngestServiceName = "zke-agent-metrics-ingest"
	IngestPortName    = "metrics-ingest"
	IngestPort        = 8429
	IngestWritePath   = "/api/v1/write"

	CollectorName          = "zke-metrics-collector"
	CollectorConfigMapName = "zke-metrics-collector-config"
	CollectorConfigKey     = "scrape.yaml"
	CollectorContainerName = "vmagent"

	// The two additional scrape targets. They are installed and removed with
	// the collector rather than separately: a target nothing scrapes is waste,
	// and a scrape configuration pointing at something that was never installed
	// only produces failing targets. One operation, one set of objects.
	KubeStateName          = "zke-metrics-kube-state"
	KubeStateContainerName = "kube-state-metrics"
	KubeStatePort          = 8080

	NodeExporterName          = "zke-metrics-node-exporter"
	NodeExporterContainerName = "node-exporter"
	NodeExporterPort          = 9100
)

// Component identifies one of the three workloads an install puts into a
// Cluster. The values travel to the Console, which uses them to say which
// target a query needs, so they are part of the API rather than internal names.
const (
	ComponentCollector    = "collector"
	ComponentKubeState    = "kube-state-metrics"
	ComponentNodeExporter = "node-exporter"
)

// Components lists them in install order: the collector first, because the
// other two exist only to be scraped by it.
func Components() []string {
	return []string{ComponentCollector, ComponentKubeState, ComponentNodeExporter}
}

// ObjectName reports the Kubernetes object name one component's objects share.
func ObjectName(component string) string {
	switch component {
	case ComponentKubeState:
		return KubeStateName
	case ComponentNodeExporter:
		return NodeExporterName
	default:
		return CollectorName
	}
}

// CollectorLabels marks everything the Agent installs for metrics collection.
//
// The Agent refuses to modify or delete an object carrying these names but not
// these labels: a Cluster may already run its own collector, and adopting it
// silently would be worse than refusing to install.
func CollectorLabels() map[string]string {
	return ComponentLabels(ComponentCollector)
}

// ComponentLabels is the label set for one component's objects.
//
// `app.kubernetes.io/name` differs per component because it is also the
// Deployment and DaemonSet selector, and two workloads sharing a selector would
// each try to own the other's Pods. The ownership label is the same for all
// three: it is what the install and uninstall paths check before touching
// anything.
func ComponentLabels(component string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       ObjectName(component),
		"app.kubernetes.io/component":  "metrics",
		"app.kubernetes.io/managed-by": ManagedByAgent,
	}
}

// ManagedByLabel is the single label the ownership check reads.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByAgent = "zke-agent"
)
