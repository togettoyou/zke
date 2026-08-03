package kubernetescatalog

// Catalog is the stable, transport-independent subset of Kubernetes API
// discovery that the Server exposes to resource browsers. Partial means one or
// more API groups could not be discovered, while Resources still contains the
// groups that succeeded.
type Catalog struct {
	Resources []Resource `json:"resources"`
	Partial   bool       `json:"partial"`
	// CustomResourcesKnown reports whether the Agent could read the cluster's
	// CustomResourceDefinitions. Kubernetes discovery does not say which
	// resources come from a CRD, so the answer requires a second read that may
	// be unavailable — and when it is, every Resource.CustomResource below is
	// false because it is unknown, not because it is false.
	CustomResourcesKnown bool `json:"custom_resources_known"`
}

// Resource describes one primary Kubernetes API resource. Subresources are
// intentionally omitted from the Phase 2 read-only catalog.
type Resource struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Resource   string   `json:"resource"`
	Kind       string   `json:"kind"`
	Namespaced bool     `json:"namespaced"`
	Verbs      []string `json:"verbs"`
	ShortNames []string `json:"short_names,omitempty"`
	Categories []string `json:"categories,omitempty"`
	// CustomResource is true when a CustomResourceDefinition in this cluster
	// serves this group and plural name. Only meaningful while the catalog
	// reports CustomResourcesKnown.
	CustomResource bool `json:"custom_resource"`
}
