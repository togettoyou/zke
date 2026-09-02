package aitools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/airuntime"
	"github.com/togettoyou/zke/pkg/server/metricsquery"
)

type catalogueMetricsStub struct {
	customMetricsStub
	definitions []metricsquery.Definition
}

func (stub *catalogueMetricsStub) Catalog() []metricsquery.Definition { return stub.definitions }

// Every page must fit whole. Pagination makes a broad catalogue discoverable;
// pruning one page would still hide arbitrary query names without saying so.
func TestMetricsCataloguePagesFitOneToolResult(t *testing.T) {
	t.Parallel()

	definitions := metricsquery.Catalog()
	for offset := 0; offset < len(definitions); offset += maxMetricsCataloguePage {
		end := min(offset+maxMetricsCataloguePage, len(definitions))
		listing := MetricsCatalogueListing(definitions[offset:end])
		if size := len([]rune(listing)); size >= DefaultResultThresholdRunes {
			t.Fatalf(
				"catalogue page %d-%d is %d runes, which the default threshold of %d cuts",
				offset, end, size, DefaultResultThresholdRunes,
			)
		}
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

func TestMetricsCatalogueSearchFindsExpandedMonitoringAreas(t *testing.T) {
	t.Parallel()

	for search, want := range map[string]string{
		"CoreDNS":            "coredns_requests",
		"控制面":                "control_plane_up",
		"工作负载网络":             "workload_network_receive",
		"GPU":                "gpu_utilization",
		"kube-state-metrics": "cluster_cpu_requests",
	} {
		matched := filterMetricQueries(metricsquery.Catalog(), search)
		found := false
		for _, definition := range matched {
			if definition.Name == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("search %q did not find %q", search, want)
		}
	}
}

func TestListMetricQueriesReturnsExplicitSearchPagination(t *testing.T) {
	t.Parallel()

	stub := &catalogueMetricsStub{definitions: metricsquery.Catalog()}
	catalogue := New(Dependencies{Metrics: stub}, Config{})
	result, err := catalogue.Invoke(context.Background(), airuntime.ToolInvocation{
		Name: toolListMetricQueries, Arguments: json.RawMessage(`{"search":"GPU","limit":2}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Text, "gpu_utilization") ||
		!strings.Contains(result.Text, "offset=2") ||
		strings.Contains(result.Text, "结果中间已省略") {
		t.Fatalf("paged search result = %q", result.Text)
	}
	if !catalogue.HasMetricQuery("gpu_utilization") || catalogue.HasMetricQuery("gpu_missing") {
		t.Fatal("metric view validation does not use the query catalogue")
	}
}
