package metricsquery

import (
	"strings"
	"testing"
)

func TestExpandedCatalogIsUniqueAndBuildsCleanExpressions(t *testing.T) {
	t.Parallel()

	definitions := Catalog()
	if len(definitions) < 180 {
		t.Fatalf("Catalog() has %d queries, want at least 180", len(definitions))
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		if _, exists := seen[definition.Name]; exists {
			t.Fatalf("duplicate query name %q", definition.Name)
		}
		seen[definition.Name] = struct{}{}
		expression := definition.build(`zke_cluster_id="cluster"`, buildParams{
			Namespace: "default",
			Top:       10,
			Window:    "5m",
		})
		if strings.Contains(expression, "%!") || strings.TrimSpace(expression) == "" {
			t.Fatalf("query %q built an invalid expression: %s", definition.Name, expression)
		}
	}
}
