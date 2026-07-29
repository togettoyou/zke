package kubernetescatalog

// Catalog is the stable, transport-independent subset of Kubernetes API
// discovery that the Server exposes to resource browsers. Partial means one or
// more API groups could not be discovered, while Resources still contains the
// groups that succeeded.
type Catalog struct {
	Resources []Resource `json:"resources"`
	Partial   bool       `json:"partial"`
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
}
