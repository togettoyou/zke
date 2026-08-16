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
)

// CollectorLabels marks everything the Agent installs for metrics collection.
//
// The Agent refuses to modify or delete an object carrying these names but not
// these labels: a Cluster may already run its own collector, and adopting it
// silently would be worse than refusing to install.
func CollectorLabels() map[string]string {
	return map[string]string{
		"app.kubernetes.io/name":       CollectorName,
		"app.kubernetes.io/component":  "metrics",
		"app.kubernetes.io/managed-by": "zke-agent",
	}
}

// ManagedByLabel is the single label the ownership check reads.
const (
	ManagedByLabel = "app.kubernetes.io/managed-by"
	ManagedByAgent = "zke-agent"
)
