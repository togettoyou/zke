package helm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// A chart whose author declared what a valid configuration is: replicaCount has
// to be an integer, and `image` has to be there at all.
const testValuesSchema = `{
  "$schema": "https://json-schema.org/draft-07/schema#",
  "type": "object",
  "required": ["image"],
  "properties": {
    "replicaCount": { "type": "integer" },
    "image": { "type": "string" }
  }
}`

func schemaChartService(t *testing.T) (*Service, *recordingAgent, *repositoryServer) {
	t.Helper()
	archive := chartArchiveWith(t, "demo", "1.2.0", map[string]string{
		"values.schema.json": testValuesSchema,
		"values.yaml":        "replicaCount: 1\nimage: demo:1\n",
	})
	server := newRepositoryServer(t, archive)
	service, agent := newTestService(t, testRepository(server.URL))
	return service, agent, server
}

func installWithValues(service *Service, values string) error {
	_, err := service.Install(context.Background(), InstallInput{
		ClusterID:      testClusterID,
		Namespace:      "shop",
		Name:           "checkout",
		RepositoryID:   testRepositoryID,
		Chart:          "demo",
		Version:        "1.2.0",
		Values:         values,
		IdempotencyKey: "key-1",
	})
	return err
}

// Values the chart's own schema rejects are refused here, before a Cluster is
// contacted — so the operator reads the failing key rather than a rendering
// error returned across a Stream, and the idempotency key is not spent.
func TestValuesAreCheckedAgainstTheChartSchema(t *testing.T) {
	t.Parallel()

	service, agent, _ := schemaChartService(t)

	err := installWithValues(service, "replicaCount: many\nimage: demo:1\n")
	if !errors.Is(err, ErrValuesRejected) {
		t.Fatalf("Install() = %v, want ErrValuesRejected", err)
	}
	if agent.request != nil {
		t.Fatal("Install() reached the Agent with values the chart's schema rejects")
	}
	// The schema's own account travels with the refusal. "values are invalid"
	// would leave an operator reading their whole document.
	var detailed interface{ Detail() string }
	if !errors.As(err, &detailed) || !strings.Contains(detailed.Detail(), "replicaCount") {
		t.Fatalf("Install() error did not name the failing key: %v", err)
	}
}

// The defaults count. A schema that requires a key the chart's own values.yaml
// supplies must not refuse an operator who did not repeat it, which is why the
// check runs against the coalesced document rather than against what was typed.
func TestValuesAreCheckedAfterTheChartDefaultsAreMerged(t *testing.T) {
	t.Parallel()

	service, agent, _ := schemaChartService(t)

	if err := installWithValues(service, "replicaCount: 3\n"); err != nil {
		t.Fatalf("Install() = %v, want the chart's default image to satisfy the schema", err)
	}
	if agent.request == nil {
		t.Fatal("Install() did not reach the Agent")
	}
}

// An upgrade that reuses the previous revision's values is validated by Helm on
// the Agent alone: the document that will be rendered is a merge with values
// this Server does not read, so checking the half in hand would refuse valid
// upgrades.
func TestReusedValuesSkipTheServerSideSchemaCheck(t *testing.T) {
	t.Parallel()

	service, agent, _ := schemaChartService(t)

	_, err := service.Upgrade(context.Background(), UpgradeInput{
		InstallInput: InstallInput{
			ClusterID:      testClusterID,
			Namespace:      "shop",
			Name:           "checkout",
			RepositoryID:   testRepositoryID,
			Chart:          "demo",
			Version:        "1.2.0",
			Values:         "replicaCount: many\n",
			IdempotencyKey: "key-1",
		},
		ReuseValues: true,
	})
	if err != nil {
		t.Fatalf("Upgrade(reuse_values) = %v, want the check deferred to the Agent", err)
	}
	if agent.request == nil || !agent.request.GetReuseValues() {
		t.Fatal("Upgrade(reuse_values) did not reach the Agent")
	}
}

// The schema is returned with the chart so the editor that has to satisfy it
// can show what it requires, rather than leaving it to be discovered from a
// rejection.
func TestChartDetailCarriesTheValuesSchema(t *testing.T) {
	t.Parallel()

	service, _, _ := schemaChartService(t)

	detail, err := service.GetChart(context.Background(), testRepositoryID, "demo", "1.2.0")
	if err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if !strings.Contains(detail.ValuesSchema, `"replicaCount"`) {
		t.Fatalf("chart detail values_schema = %q", detail.ValuesSchema)
	}
}

// A chart that packages no schema is not validated at all, and installing one
// costs nothing extra. Most charts are in this case.
func TestChartWithoutASchemaIsNotValidated(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))

	if err := installWithValues(service, "anything: at all\n"); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if agent.request == nil {
		t.Fatal("Install() did not reach the Agent")
	}
}
