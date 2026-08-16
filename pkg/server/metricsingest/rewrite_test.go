package metricsingest

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protowire"
)

// The tests build remote write payloads by hand rather than depending on the
// Prometheus client libraries. The format is frozen, and encoding it here also
// keeps the fixtures explicit about what the Server is expected to tolerate.
type testSample struct {
	value       float64
	timestampMS int64
}

type testSeries struct {
	labels  []label
	samples []testSample
	extra   []byte
}

func encodeSeries(series testSeries) []byte {
	encoded := []byte{}
	for _, current := range series.labels {
		inner := protowire.AppendTag(nil, labelNameField, protowire.BytesType)
		inner = protowire.AppendString(inner, current.name)
		inner = protowire.AppendTag(inner, labelValueField, protowire.BytesType)
		inner = protowire.AppendString(inner, current.value)
		encoded = protowire.AppendTag(encoded, timeSeriesLabelsField, protowire.BytesType)
		encoded = protowire.AppendBytes(encoded, inner)
	}
	for _, sample := range series.samples {
		inner := protowire.AppendTag(nil, 1, protowire.Fixed64Type)
		inner = protowire.AppendFixed64(inner, uint64(int64(sample.value)))
		inner = protowire.AppendTag(inner, sampleTimestampField, protowire.VarintType)
		inner = protowire.AppendVarint(inner, uint64(sample.timestampMS))
		encoded = protowire.AppendTag(encoded, timeSeriesSamplesField, protowire.BytesType)
		encoded = protowire.AppendBytes(encoded, inner)
	}
	return append(encoded, series.extra...)
}

func encodeWriteRequest(series ...testSeries) []byte {
	encoded := []byte{}
	for _, current := range series {
		encoded = protowire.AppendTag(
			encoded,
			writeRequestTimeSeriesField,
			protowire.BytesType,
		)
		encoded = protowire.AppendBytes(encoded, encodeSeries(current))
	}
	return encoded
}

func decodeLabels(t *testing.T, payload []byte) [][]label {
	t.Helper()
	var all [][]label
	rest := payload
	for len(rest) > 0 {
		number, kind, consumed := protowire.ConsumeTag(rest)
		if consumed < 0 {
			t.Fatal("malformed rewritten payload")
		}
		rest = rest[consumed:]
		value, size := protowire.ConsumeBytes(rest)
		if size < 0 {
			t.Fatal("malformed rewritten time series")
		}
		rest = rest[size:]
		if number != writeRequestTimeSeriesField || kind != protowire.BytesType {
			continue
		}
		var labels []label
		inner := value
		for len(inner) > 0 {
			innerNumber, innerKind, innerConsumed := protowire.ConsumeTag(inner)
			if innerConsumed < 0 {
				t.Fatal("malformed rewritten series field")
			}
			inner = inner[innerConsumed:]
			innerValue, innerSize := protowire.ConsumeBytes(inner)
			if innerSize < 0 {
				t.Fatal("malformed rewritten series value")
			}
			inner = inner[innerSize:]
			if innerNumber != timeSeriesLabelsField || innerKind != protowire.BytesType {
				continue
			}
			parsed, err := parseLabel(innerValue, testLimits())
			if err != nil {
				t.Fatal(err)
			}
			labels = append(labels, parsed)
		}
		all = append(all, labels)
	}
	return all
}

func testLimits() Limits {
	return Limits{
		MaxSeriesPerBatch:  4,
		MaxSamplesPerBatch: 8,
		MaxLabelsPerSeries: 8,
		MaxLabelNameBytes:  64,
		MaxLabelValueBytes: 128,
		MaxSampleAge:       time.Hour,
		MaxSampleFuture:    time.Minute,
	}
}

func TestRewriteReplacesScopeLabelsTheClusterSent(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	payload := encodeWriteRequest(testSeries{
		labels: []label{
			{name: "__name__", value: "node_cpu_seconds_total"},
			// A Cluster claiming to be another one, plus a scope label ZKE
			// does not even store. Both must disappear.
			{name: ClusterLabel, value: "cls_impersonated"},
			{name: "zke_tenant_id", value: "tenant_impersonated"},
			{name: "node", value: "node-1"},
		},
		samples: []testSample{{value: 3, timestampMS: now.UnixMilli()}},
	})

	rewritten, stats, err := rewriteBatch(
		payload,
		[]label{{name: ClusterLabel, value: "cls_real"}},
		testLimits(),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.series != 1 || stats.samples != 1 {
		t.Fatalf("stats = %+v", stats)
	}
	labels := decodeLabels(t, rewritten)
	if len(labels) != 1 {
		t.Fatalf("rewritten series = %d, want 1", len(labels))
	}
	want := []label{
		{name: "__name__", value: "node_cpu_seconds_total"},
		{name: "node", value: "node-1"},
		{name: ClusterLabel, value: "cls_real"},
	}
	if len(labels[0]) != len(want) {
		t.Fatalf("labels = %+v, want %+v", labels[0], want)
	}
	for index, expected := range want {
		if labels[0][index] != expected {
			t.Fatalf("label %d = %+v, want %+v", index, labels[0][index], expected)
		}
	}
}

func TestRewriteSortsTheScopeLabelIntoPlace(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	payload := encodeWriteRequest(testSeries{
		labels: []label{
			{name: "__name__", value: "up"},
			// Sorts after "zke_cluster_id", so appending the scope label
			// without sorting would emit an out-of-order label set.
			{name: "zone", value: "b"},
		},
		samples: []testSample{{value: 1, timestampMS: now.UnixMilli()}},
	})
	rewritten, _, err := rewriteBatch(
		payload,
		[]label{{name: ClusterLabel, value: "cls_real"}},
		testLimits(),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	labels := decodeLabels(t, rewritten)[0]
	if labels[1].name != ClusterLabel || labels[2].name != "zone" {
		t.Fatalf("labels are not sorted by name: %+v", labels)
	}
}

func TestRewriteKeepsSamplesAndUnknownFields(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	// Field 3 of TimeSeries carries exemplars, which this Server does not
	// interpret. Dropping unknown fields would silently discard payload
	// features, so they must survive the rewrite untouched.
	extra := protowire.AppendTag(nil, 3, protowire.BytesType)
	extra = protowire.AppendBytes(extra, []byte("opaque"))
	payload := encodeWriteRequest(testSeries{
		labels: []label{{name: "__name__", value: "up"}},
		samples: []testSample{
			{value: 1, timestampMS: now.UnixMilli()},
			{value: 2, timestampMS: now.Add(-time.Minute).UnixMilli()},
		},
		extra: extra,
	})
	rewritten, stats, err := rewriteBatch(
		payload,
		[]label{{name: ClusterLabel, value: "cls_real"}},
		testLimits(),
		now,
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.samples != 2 {
		t.Fatalf("samples = %d, want 2", stats.samples)
	}
	if !containsSubslice(rewritten, []byte("opaque")) {
		t.Fatal("unknown time series field was dropped")
	}
}

func TestRewriteRejectsMalformedAndOversizedBatches(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	scope := []label{{name: ClusterLabel, value: "cls_real"}}
	valid := testSample{value: 1, timestampMS: now.UnixMilli()}

	cases := map[string]struct {
		payload []byte
		want    error
	}{
		"no metric name": {
			payload: encodeWriteRequest(testSeries{
				labels:  []label{{name: "node", value: "node-1"}},
				samples: []testSample{valid},
			}),
			want: ErrPayloadInvalid,
		},
		"repeated label": {
			payload: encodeWriteRequest(testSeries{
				labels: []label{
					{name: "__name__", value: "up"},
					{name: "node", value: "a"},
					{name: "node", value: "b"},
				},
				samples: []testSample{valid},
			}),
			want: ErrPayloadInvalid,
		},
		"invalid label name": {
			payload: encodeWriteRequest(testSeries{
				labels: []label{
					{name: "__name__", value: "up"},
					{name: "node-1", value: "a"},
				},
				samples: []testSample{valid},
			}),
			want: ErrPayloadInvalid,
		},
		"future sample": {
			payload: encodeWriteRequest(testSeries{
				labels:  []label{{name: "__name__", value: "up"}},
				samples: []testSample{{value: 1, timestampMS: now.Add(time.Hour).UnixMilli()}},
			}),
			want: ErrPayloadInvalid,
		},
		"ancient sample": {
			payload: encodeWriteRequest(testSeries{
				labels:  []label{{name: "__name__", value: "up"}},
				samples: []testSample{{value: 1, timestampMS: now.Add(-48 * time.Hour).UnixMilli()}},
			}),
			want: ErrPayloadInvalid,
		},
		"truncated payload": {
			payload: []byte{0x0a, 0x7f},
			want:    ErrPayloadInvalid,
		},
		"too many series": {
			payload: encodeWriteRequest(
				testSeries{labels: []label{{name: "__name__", value: "a"}}, samples: []testSample{valid}},
				testSeries{labels: []label{{name: "__name__", value: "b"}}, samples: []testSample{valid}},
				testSeries{labels: []label{{name: "__name__", value: "c"}}, samples: []testSample{valid}},
				testSeries{labels: []label{{name: "__name__", value: "d"}}, samples: []testSample{valid}},
				testSeries{labels: []label{{name: "__name__", value: "e"}}, samples: []testSample{valid}},
			),
			want: ErrPayloadTooLarge,
		},
		"too many labels": {
			payload: encodeWriteRequest(testSeries{
				labels: []label{
					{name: "__name__", value: "up"},
					{name: "a", value: "1"},
					{name: "b", value: "2"},
					{name: "c", value: "3"},
					{name: "d", value: "4"},
					{name: "e", value: "5"},
					{name: "f", value: "6"},
					{name: "g", value: "7"},
				},
				samples: []testSample{valid},
			}),
			want: ErrPayloadTooLarge,
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := rewriteBatch(
				testCase.payload,
				scope,
				testLimits(),
				now,
			); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

// Collectors send metric metadata — HELP and TYPE — in requests carrying no
// time series at all, and vmagent does so from its first scrape onwards.
// Refusing them made a steady stream of valid traffic come back as "the Server
// rejected this" while everything else looked fine.
func TestRewriteAcceptsMetadataOnlyBatches(t *testing.T) {
	t.Parallel()

	// WriteRequest.metadata is field 3; the batch has no field 1 at all.
	metadata := protowire.AppendTag(nil, 1, protowire.VarintType)
	metadata = protowire.AppendVarint(metadata, 1)
	metadata = protowire.AppendTag(metadata, 2, protowire.BytesType)
	metadata = protowire.AppendString(metadata, "node_cpu_usage_seconds_total")
	payload := protowire.AppendTag(nil, 3, protowire.BytesType)
	payload = protowire.AppendBytes(payload, metadata)

	rewritten, stats, err := rewriteBatch(
		payload,
		[]label{{name: ClusterLabel, value: "cls_real"}},
		testLimits(),
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if stats.series != 0 || stats.samples != 0 {
		t.Fatalf("stats = %+v", stats)
	}
	// Nothing to rewrite means nothing changed: the metadata reaches storage
	// exactly as the collector sent it.
	if string(rewritten) != string(payload) {
		t.Fatal("metadata-only batch was altered on the way through")
	}
}

func containsSubslice(haystack []byte, needle []byte) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if string(haystack[index:index+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
