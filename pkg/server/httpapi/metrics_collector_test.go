package httpapi

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/metricscollector"
)

// The contract declares scrape_jobs and targets as arrays, and the Console
// reads their length without guarding. A Job with no targets is ordinary — a
// built-in Node job has no fixed list, and an annotated Service with no ready
// backend has none yet — so it has to serialize as [] rather than null.
func TestScrapeJobsSerializeEmptyListsAsArrays(t *testing.T) {
	t.Parallel()

	encoded, err := json.Marshal(metricsCollectorStateResponse{
		Components: componentStatesResponse(nil),
		ScrapeJobs: scrapeJobsResponse([]metricscollector.ScrapeJob{{
			JobName: "node-exporter", SourceKind: "Builtin",
			Scheme: "http", MetricsPath: "/metrics",
		}}),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{`"targets":null`, `"components":null`, `"scrape_jobs":null`} {
		if strings.Contains(string(encoded), absent) {
			t.Fatalf("response carries %s:\n%s", absent, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"targets":[]`) {
		t.Fatalf("a Job without targets did not serialize an empty array:\n%s", encoded)
	}
	if strings.Contains(string(encoded), `"scrape_jobs":[]`) {
		t.Fatalf("the Job itself was dropped:\n%s", encoded)
	}
}
