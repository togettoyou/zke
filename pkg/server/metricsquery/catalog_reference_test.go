package metricsquery

import (
	"strings"
	"testing"

	"github.com/VictoriaMetrics/metricsql"
	"github.com/togettoyou/zke/pkg/server/metricsqlguard"
)

func TestExpandedCatalogIsUniqueAndBuildsCleanExpressions(t *testing.T) {
	t.Parallel()

	definitions := Catalog()
	if len(definitions) != 396 {
		t.Fatalf("Catalog() has %d queries, want 396; update the coverage docs with intentional additions", len(definitions))
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
		if _, err := metricsql.Parse(expression); err != nil {
			t.Fatalf("query %q built an unparseable expression %q: %v", definition.Name, expression, err)
		}
		display, err := metricsqlguard.WithoutLabel(expression, "zke_cluster_id")
		if err != nil {
			t.Fatalf("query %q has no portable expression: %v", definition.Name, err)
		}
		if strings.Contains(display, "zke_cluster_id") {
			t.Fatalf("query %q portable expression leaks cluster label: %s", definition.Name, display)
		}
	}
}
