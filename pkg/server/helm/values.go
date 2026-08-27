package helm

import (
	"bytes"
	"errors"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"sigs.k8s.io/yaml"
)

// Checking values against the chart's own schema, before a Cluster is involved.
//
// A chart may package a `values.schema.json`, and when it does, that file is the
// chart author's statement of what a valid configuration is: which keys are
// required, what type each holds, which enumerations are accepted. Helm applies
// it while rendering — on the Agent, at the far end of a Stream, after this
// Server has fetched the chart and after an idempotency key has been handed out.
//
// Applying it here as well moves the failure to the front of the operation. What
// that buys is not a second opinion — it is the same check, run with the same
// library Helm runs it with — but three things about *where* it happens:
//
//   - The operator finds out before a Cluster is contacted, so a mistyped value
//     costs a form error rather than a round trip and a rejected release.
//   - No idempotency key is spent. A refusal here provably wrote nothing, which
//     is exactly the class the Agent's replay cache also declines to reserve.
//   - The Cluster's Agent is not asked to render a chart that cannot render.
//
// Helm on the Agent stays the authority. This never admits what Helm would
// refuse — it runs Helm's own CoalesceValues and ValidateAgainstSchema — and
// where it cannot see the same document it does not guess: an upgrade that
// reuses the previous revision's values is validated on the Agent alone, because
// the values being validated live in release storage this Server does not read.

// ErrValuesRejected is a values document the chart's own schema refuses.
var ErrValuesRejected = errors.New("values do not satisfy the chart's schema")

// The longest schema complaint returned to a caller. Helm reports every failed
// constraint, and a schema with many required fields produces a wall of them;
// past this the reader is scrolling rather than reading, and the whole of it is
// still available by validating locally.
const maxSchemaErrorLength = 4 << 10

// valuesError carries the schema's own account of what is wrong. The detail is
// the point: "values are invalid" tells an operator nothing, and the failing
// key path tells them what to change.
type valuesError struct {
	detail string
}

func (err *valuesError) Error() string  { return err.detail }
func (err *valuesError) Detail() string { return err.detail }
func (err *valuesError) Unwrap() error  { return ErrValuesRejected }

// validateValues checks one values document against the chart it will be
// rendered with.
//
// The chart is parsed here from the archive that is about to be sent rather
// than taken from the in-memory chart cache. Coalescing merges the chart's own
// defaults into a copy of the values, and handing a shared parse to that is a
// way for one request's values to end up in another's — a fresh parse of an
// archive already in hand costs milliseconds and removes the question.
func (service *Service) validateValues(archive []byte, values []byte, skip bool) error {
	if skip {
		return nil
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(archive))
	if err != nil {
		return unreachable("chart archive could not be read: %s", err)
	}
	if !chartHasSchema(loaded) {
		return nil
	}
	// An empty document is validated too, not skipped: a schema exists partly
	// to say which values have no default and must be supplied.
	var supplied map[string]any
	if len(values) > 0 {
		if err := yaml.Unmarshal(values, &supplied); err != nil {
			return invalid("values document is not a YAML mapping")
		}
	}
	coalesced, err := chartutil.CoalesceValues(loaded, supplied)
	if err != nil {
		return invalid("values could not be merged with the chart's defaults: %s", err)
	}
	if err := chartutil.ValidateAgainstSchema(loaded, coalesced); err != nil {
		return &valuesError{detail: truncateText(err.Error(), maxSchemaErrorLength)}
	}
	return nil
}

// chartHasSchema reports whether anything in this chart would be validated.
//
// Subcharts are included because Helm validates them: installing a chart
// installs its dependencies, and a dependency's schema applies to the section
// of the document that configures it.
func chartHasSchema(loaded *chart.Chart) bool {
	if loaded == nil {
		return false
	}
	if len(loaded.Schema) > 0 {
		return true
	}
	for _, dependency := range loaded.Dependencies() {
		if chartHasSchema(dependency) {
			return true
		}
	}
	return false
}

// chartValuesSchema is the chart's own `values.schema.json`, verbatim, for the
// Console to show beside the editor. It is returned as text for the same reason
// values.yaml is: it is a document written to be read.
func chartValuesSchema(loaded *chart.Chart) string {
	if loaded == nil || len(loaded.Schema) == 0 {
		return ""
	}
	return truncateText(string(loaded.Schema), maxChartSchemaBytes)
}
