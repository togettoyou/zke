package metricsingest

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/protobuf/encoding/protowire"
)

// ScopeLabelPrefix is reserved for ZKE. Every label carrying it is removed
// from an incoming batch before the Server writes its own, so a Cluster cannot
// present itself as another one by shipping the label itself.
const ScopeLabelPrefix = "zke_"

// ClusterLabel is the only scope label stored with the samples. Tenant and
// Project are deliberately absent: a Cluster's ownership is a mutable
// relationship in the Server database, and a label written today cannot be
// corrected when that relationship changes.
const ClusterLabel = ScopeLabelPrefix + "cluster_id"

// Prometheus remote write 1.0 field numbers. The format is frozen, so these
// are stable. Everything this code does not need to understand is copied
// through byte for byte instead of being decoded and re-encoded, which keeps
// payload features it has never heard of intact.
const (
	writeRequestTimeSeriesField protowire.Number = 1
	timeSeriesLabelsField       protowire.Number = 1
	timeSeriesSamplesField      protowire.Number = 2
	labelNameField              protowire.Number = 1
	labelValueField             protowire.Number = 2
	sampleTimestampField        protowire.Number = 2
)

var (
	// ErrPayloadInvalid marks a batch the Server refuses on its merits.
	// Replaying it unchanged would fail again, so it must never carry a retry
	// hint back to the collector.
	ErrPayloadInvalid = errors.New("metrics payload is invalid")
	// ErrPayloadTooLarge marks a batch that is well formed but larger than a
	// single batch is allowed to be. Splitting it is the collector's job.
	ErrPayloadTooLarge = errors.New("metrics payload exceeds a configured limit")
)

// Limits bound one batch. They protect the Server and the storage backend from
// a single Cluster, so they are enforced before anything is forwarded.
type Limits struct {
	MaxSeriesPerBatch  int
	MaxSamplesPerBatch int
	MaxLabelsPerSeries int
	MaxLabelNameBytes  int
	MaxLabelValueBytes int
	// Sample timestamps outside this window are rejected. Future samples
	// corrupt range queries and are usually a clock problem; very old samples
	// arrive as backfill the retention window may not even cover.
	MaxSampleAge    time.Duration
	MaxSampleFuture time.Duration
}

type label struct {
	name  string
	value string
}

type batchStats struct {
	series  int
	samples int
	// hashes identify the series this batch carried, so the per-Cluster
	// cardinality budget can be charged without parsing the payload a second
	// time. They are only ever fed to a sketch, never compared for equality
	// across batches, so a collision costs a slightly low estimate and nothing
	// else.
	hashes []uint64
}

// rewriteBatch enforces the limits and replaces the scope identity of every
// series in an uncompressed remote write request.
func rewriteBatch(
	payload []byte,
	scope []label,
	limits Limits,
	now time.Time,
) ([]byte, batchStats, error) {
	var stats batchStats
	rewritten := make([]byte, 0, len(payload)+len(payload)/8)
	rest := payload
	for len(rest) > 0 {
		number, kind, consumed := protowire.ConsumeTag(rest)
		if consumed < 0 {
			return nil, stats, fmt.Errorf("%w: malformed field tag", ErrPayloadInvalid)
		}
		rest = rest[consumed:]
		if number == writeRequestTimeSeriesField && kind == protowire.BytesType {
			series, size := protowire.ConsumeBytes(rest)
			if size < 0 {
				return nil, stats, fmt.Errorf(
					"%w: malformed time series",
					ErrPayloadInvalid,
				)
			}
			rest = rest[size:]
			stats.series++
			if stats.series > limits.MaxSeriesPerBatch {
				return nil, stats, fmt.Errorf(
					"%w: batch carries more than %d series",
					ErrPayloadTooLarge,
					limits.MaxSeriesPerBatch,
				)
			}
			replacement, err := rewriteTimeSeries(series, scope, limits, now, &stats)
			if err != nil {
				return nil, stats, err
			}
			rewritten = protowire.AppendTag(rewritten, number, protowire.BytesType)
			rewritten = protowire.AppendBytes(rewritten, replacement)
			continue
		}
		if number == writeRequestTimeSeriesField {
			return nil, stats, fmt.Errorf(
				"%w: time series has an unexpected wire type",
				ErrPayloadInvalid,
			)
		}
		size := protowire.ConsumeFieldValue(number, kind, rest)
		if size < 0 {
			return nil, stats, fmt.Errorf("%w: malformed field", ErrPayloadInvalid)
		}
		rewritten = protowire.AppendTag(rewritten, number, kind)
		rewritten = append(rewritten, rest[:size]...)
		rest = rest[size:]
	}
	// A batch with no time series is not an error. Collectors send metric
	// metadata — HELP and TYPE, `WriteRequest.metadata` — in requests of their
	// own, and vmagent does so from its very first scrape. Rejecting those made
	// every other batch look fine while a steady stream of valid ones came back
	// as "the Server rejected this".
	return rewritten, stats, nil
}

func rewriteTimeSeries(
	series []byte,
	scope []label,
	limits Limits,
	now time.Time,
	stats *batchStats,
) ([]byte, error) {
	labels := make([]label, 0, 16)
	// Samples and anything else keep their original order and encoding; only
	// the label set is rebuilt.
	tail := make([]byte, 0, len(series))
	rest := series
	for len(rest) > 0 {
		number, kind, consumed := protowire.ConsumeTag(rest)
		if consumed < 0 {
			return nil, fmt.Errorf("%w: malformed series field tag", ErrPayloadInvalid)
		}
		rest = rest[consumed:]
		switch {
		case number == timeSeriesLabelsField && kind == protowire.BytesType:
			value, size := protowire.ConsumeBytes(rest)
			if size < 0 {
				return nil, fmt.Errorf("%w: malformed label", ErrPayloadInvalid)
			}
			rest = rest[size:]
			parsed, err := parseLabel(value, limits)
			if err != nil {
				return nil, err
			}
			if strings.HasPrefix(parsed.name, ScopeLabelPrefix) {
				// Dropped, not rejected: a collector may legitimately be
				// scraping something that already uses the prefix, and the
				// Server's own value replaces it either way.
				continue
			}
			if parsed.value == "" {
				// An empty value means the label is absent in Prometheus data
				// model terms; keeping it would only create a second identity
				// for the same series.
				continue
			}
			labels = append(labels, parsed)
		case number == timeSeriesSamplesField && kind == protowire.BytesType:
			value, size := protowire.ConsumeBytes(rest)
			if size < 0 {
				return nil, fmt.Errorf("%w: malformed sample", ErrPayloadInvalid)
			}
			rest = rest[size:]
			if err := validateSample(value, limits, now); err != nil {
				return nil, err
			}
			stats.samples++
			if stats.samples > limits.MaxSamplesPerBatch {
				return nil, fmt.Errorf(
					"%w: batch carries more than %d samples",
					ErrPayloadTooLarge,
					limits.MaxSamplesPerBatch,
				)
			}
			tail = protowire.AppendTag(tail, number, kind)
			tail = protowire.AppendBytes(tail, value)
		case number == timeSeriesLabelsField:
			// A label field that is not a length-delimited message cannot be
			// decoded, and copying it through would let an undecodable label
			// past the scope rewrite.
			return nil, fmt.Errorf("%w: label has an unexpected wire type", ErrPayloadInvalid)
		default:
			size := protowire.ConsumeFieldValue(number, kind, rest)
			if size < 0 {
				return nil, fmt.Errorf("%w: malformed series field", ErrPayloadInvalid)
			}
			tail = protowire.AppendTag(tail, number, kind)
			tail = append(tail, rest[:size]...)
			rest = rest[size:]
		}
	}
	labels = append(labels, scope...)
	if len(labels) > limits.MaxLabelsPerSeries {
		return nil, fmt.Errorf(
			"%w: series carries more than %d labels",
			ErrPayloadTooLarge,
			limits.MaxLabelsPerSeries,
		)
	}
	// Remote write requires labels sorted by name. The scope label cannot
	// simply be appended: "zke_" sorts before label names starting with "zk"
	// and after everything else, so its position depends on the payload.
	slices.SortFunc(labels, func(first, second label) int {
		return strings.Compare(first.name, second.name)
	})
	named := false
	for index, current := range labels {
		if index > 0 && labels[index-1].name == current.name {
			return nil, fmt.Errorf(
				"%w: series repeats label %q",
				ErrPayloadInvalid,
				current.name,
			)
		}
		if current.name == "__name__" {
			named = true
		}
	}
	if !named {
		return nil, fmt.Errorf("%w: series has no metric name", ErrPayloadInvalid)
	}
	stats.hashes = append(stats.hashes, hashLabels(labels))

	rewritten := make([]byte, 0, len(series)+64)
	for _, current := range labels {
		encoded := protowire.AppendTag(nil, labelNameField, protowire.BytesType)
		encoded = protowire.AppendString(encoded, current.name)
		encoded = protowire.AppendTag(encoded, labelValueField, protowire.BytesType)
		encoded = protowire.AppendString(encoded, current.value)
		rewritten = protowire.AppendTag(
			rewritten,
			timeSeriesLabelsField,
			protowire.BytesType,
		)
		rewritten = protowire.AppendBytes(rewritten, encoded)
	}
	return append(rewritten, tail...), nil
}

// hashLabels identifies a series by its whole label set. The labels are already
// sorted and deduplicated here, so the same series always hashes the same way
// regardless of the order the collector sent its labels in. Separators are
// bytes that cannot occur in a label name and are rejected in a value, so
// {a="b",c="d"} and {ab="", cd=""} cannot collide by construction.
func hashLabels(labels []label) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	hash := uint64(offset64)
	write := func(value string) {
		for index := range len(value) {
			hash ^= uint64(value[index])
			hash *= prime64
		}
	}
	for _, current := range labels {
		write(current.name)
		hash ^= 0xff
		hash *= prime64
		write(current.value)
		hash ^= 0xfe
		hash *= prime64
	}
	return hash
}

func parseLabel(encoded []byte, limits Limits) (label, error) {
	var parsed label
	rest := encoded
	for len(rest) > 0 {
		number, kind, consumed := protowire.ConsumeTag(rest)
		if consumed < 0 {
			return label{}, fmt.Errorf("%w: malformed label field", ErrPayloadInvalid)
		}
		rest = rest[consumed:]
		if kind == protowire.BytesType &&
			(number == labelNameField || number == labelValueField) {
			value, size := protowire.ConsumeBytes(rest)
			if size < 0 {
				return label{}, fmt.Errorf("%w: malformed label value", ErrPayloadInvalid)
			}
			rest = rest[size:]
			if number == labelNameField {
				parsed.name = string(value)
			} else {
				parsed.value = string(value)
			}
			continue
		}
		size := protowire.ConsumeFieldValue(number, kind, rest)
		if size < 0 {
			return label{}, fmt.Errorf("%w: malformed label field", ErrPayloadInvalid)
		}
		rest = rest[size:]
	}
	if !validLabelName(parsed.name) {
		return label{}, fmt.Errorf("%w: invalid label name", ErrPayloadInvalid)
	}
	if len(parsed.name) > limits.MaxLabelNameBytes {
		return label{}, fmt.Errorf(
			"%w: label name exceeds %d bytes",
			ErrPayloadTooLarge,
			limits.MaxLabelNameBytes,
		)
	}
	if len(parsed.value) > limits.MaxLabelValueBytes {
		return label{}, fmt.Errorf(
			"%w: label value exceeds %d bytes",
			ErrPayloadTooLarge,
			limits.MaxLabelValueBytes,
		)
	}
	if !utf8.ValidString(parsed.value) {
		return label{}, fmt.Errorf("%w: label value is not valid UTF-8", ErrPayloadInvalid)
	}
	return parsed, nil
}

func validLabelName(name string) bool {
	if name == "" {
		return false
	}
	for index := range len(name) {
		character := name[index]
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character == '_':
		case character >= '0' && character <= '9':
			if index == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func validateSample(encoded []byte, limits Limits, now time.Time) error {
	var timestamp int64
	found := false
	rest := encoded
	for len(rest) > 0 {
		number, kind, consumed := protowire.ConsumeTag(rest)
		if consumed < 0 {
			return fmt.Errorf("%w: malformed sample field", ErrPayloadInvalid)
		}
		rest = rest[consumed:]
		if number == sampleTimestampField && kind == protowire.VarintType {
			value, size := protowire.ConsumeVarint(rest)
			if size < 0 {
				return fmt.Errorf("%w: malformed sample timestamp", ErrPayloadInvalid)
			}
			rest = rest[size:]
			timestamp = int64(value)
			found = true
			continue
		}
		size := protowire.ConsumeFieldValue(number, kind, rest)
		if size < 0 {
			return fmt.Errorf("%w: malformed sample field", ErrPayloadInvalid)
		}
		rest = rest[size:]
	}
	if !found || timestamp <= 0 {
		return fmt.Errorf("%w: sample has no timestamp", ErrPayloadInvalid)
	}
	sampledAt := time.UnixMilli(timestamp)
	if sampledAt.After(now.Add(limits.MaxSampleFuture)) {
		return fmt.Errorf("%w: sample timestamp is in the future", ErrPayloadInvalid)
	}
	if sampledAt.Before(now.Add(-limits.MaxSampleAge)) {
		return fmt.Errorf("%w: sample timestamp is too old", ErrPayloadInvalid)
	}
	return nil
}
