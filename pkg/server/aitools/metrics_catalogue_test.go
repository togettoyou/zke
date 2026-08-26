package aitools

import (
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/metricsquery"
)

// The query listing is the only tool result that cannot be re-read in a
// narrower form.
//
// Every other read tool answers about something the caller named — a Namespace,
// a selector, a limit — so a pruned answer is recoverable by asking for less.
// This one is the index itself: an AIOps session that cannot see a query's name
// cannot ask for it, and the prune keeps the head and the tail, so what
// disappears is the middle of the catalogue rather than the end of it. Nothing
// reports that; the model simply behaves as though those metrics do not exist.
//
// The listing therefore has to fit in one result whole, and this is the test
// that says so — the catalogue grows, and the failure it protects against is
// invisible from the outside.
func TestMetricsCatalogueListingFitsOneToolResult(t *testing.T) {
	t.Parallel()

	listing := MetricsCatalogueListing(metricsquery.Catalog())
	if size := len([]rune(listing)); size >= DefaultResultThresholdRunes {
		t.Fatalf(
			"catalogue listing is %d runes, which the default prune threshold of %d cuts; "+
				"either shorten the listing or raise the threshold deliberately",
			size,
			DefaultResultThresholdRunes,
		)
	}
}

// Every query has to appear, with the parameters a caller is refused for
// getting wrong. `top` on a query that requires it, and a Namespace on one that
// does not accept it, are both hard errors in the query service — so a listing
// that omits either flag turns a discoverable contract into a guess.
func TestMetricsCatalogueListingNamesEveryQueryAndItsParameters(t *testing.T) {
	t.Parallel()

	definitions := metricsquery.Catalog()
	listing := MetricsCatalogueListing(definitions)
	lines := strings.Split(listing, "\n")
	// The legend is written as one entry but wraps over several lines; the
	// query lines are what remains after it.
	if len(lines) < len(definitions) {
		t.Fatalf("listing has %d lines for %d queries", len(lines), len(definitions))
	}
	indexed := make(map[string]string, len(definitions))
	for _, line := range lines {
		name, _, found := strings.Cut(line, " | ")
		if found {
			indexed[strings.TrimSpace(name)] = line
		}
	}
	for _, definition := range definitions {
		line, present := indexed[definition.Name]
		if !present {
			t.Fatalf("%s is missing from the listing", definition.Name)
		}
		if !strings.Contains(line, string(definition.Unit)) {
			t.Fatalf("%s does not carry its unit: %q", definition.Name, line)
		}
		if definition.RequiresTop && !strings.Contains(line, "top!") {
			t.Fatalf("%s requires top but the listing does not say so: %q", definition.Name, line)
		}
		if definition.SupportsNamespace && !strings.Contains(line, "ns") {
			t.Fatalf("%s accepts a Namespace but the listing does not say so: %q",
				definition.Name, line)
		}
		if definition.RequiresComponent != "" && !strings.Contains(line, "ksm") &&
			!strings.Contains(line, "node") {
			t.Fatalf("%s depends on %s but the listing does not say so: %q",
				definition.Name, definition.RequiresComponent, line)
		}
	}
}
