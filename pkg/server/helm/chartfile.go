package helm

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"helm.sh/helm/v3/pkg/chart"
)

// Reading a chart before installing it.
//
// A chart's values.yaml and README answer "what does this do"; they do not
// answer "what will this actually create". That question is only answerable by
// reading the templates, and an operator who cannot read them from here reads
// them by downloading the archive somewhere else — which is the one place ZKE
// has no say over what they get.
//
// So the whole archive is browsable, exactly as the repository published it:
// the file list travels with the chart detail, because that request already
// downloaded and parsed the archive, and one file's contents are a request of
// their own, because most of them are never opened.

// ErrChartFileNotFound is a path the chart archive does not contain. It is
// separate from a missing chart: one means the chart is not there, the other
// means it is and this file is not in it.
var ErrChartFileNotFound = errors.New("file not found in this chart")

const (
	// The largest chart file returned as text. It matches the bound on
	// values.yaml and the README, because everything here has the same
	// purpose: to be read on screen.
	maxChartFileBytes = 512 << 10
	// How many entries one chart's file listing reports. A real chart has tens
	// of files; a packaged subchart tree can have hundreds. The bound is here
	// so an archive built to be pathological cannot make one response
	// arbitrarily long, and the listing says when it applied rather than
	// quietly ending early.
	maxChartFileEntries = 2000
	// The longest path accepted from a caller. Anything longer cannot name a
	// file in an archive this Server agreed to read.
	maxChartFilePathLength = 1024
	// How much of a file is inspected to decide whether it is text. Reading the
	// head is enough: an archive member that is binary is binary at the front.
	textSampleBytes = 8 << 10
)

// ChartFileEntry is one file inside a chart archive, as the browser lists it.
//
// The contents are not here. A chart with a packaged subchart carries hundreds
// of files and none of them is opened by most readers, so a listing that
// inlined them would download the whole archive into the browser to show a
// tree.
type ChartFileEntry struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	// Text says the file can be shown as text. A chart may carry a packaged
	// subchart archive or a binary asset, and offering to open one in a code
	// viewer would be offering to paste bytes into it.
	Text bool `json:"text"`
}

// ChartFileDetail is one file's contents.
type ChartFileDetail struct {
	RepositoryID string `json:"repository_id"`
	Chart        string `json:"chart"`
	Version      string `json:"version"`
	Path         string `json:"path"`
	Size         int    `json:"size"`
	Text         bool   `json:"text"`
	// Content is empty for a file that is not text: there is nothing useful to
	// show, and Size already says the file is there.
	Content string `json:"content"`
	// Truncated says Content stops before the file does. Size is the file's
	// real length, so the two together say how much was cut.
	Truncated bool `json:"truncated"`
}

// GetChartFile returns one file out of a chart archive, verbatim.
//
// The path must name a file the archive actually contains — it is matched
// against the archive's own member names rather than joined onto anything, so
// there is no path for a caller to traverse out of.
func (service *Service) GetChartFile(
	ctx context.Context,
	repositoryID string,
	chartName string,
	version string,
	path string,
) (ChartFileDetail, error) {
	path = strings.TrimSpace(path)
	if path == "" || len(path) > maxChartFilePathLength {
		return ChartFileDetail{}, ErrInvalidInput
	}
	loaded, resolved, _, err := service.loadChart(ctx, repositoryID, chartName, version)
	if err != nil {
		return ChartFileDetail{}, err
	}
	detail := ChartFileDetail{
		RepositoryID: repositoryID,
		Chart:        chartName,
		Version:      resolved,
		Path:         path,
	}
	if loaded.Metadata != nil {
		detail.Chart = loaded.Metadata.Name
		detail.Version = loaded.Metadata.Version
	}
	for _, file := range loaded.Raw {
		if file == nil || file.Name != path {
			continue
		}
		detail.Size = len(file.Data)
		detail.Text = isTextFile(file.Data)
		if !detail.Text {
			return detail, nil
		}
		detail.Content = truncateText(string(file.Data), maxChartFileBytes)
		detail.Truncated = len(detail.Content) < detail.Size
		return detail, nil
	}
	return ChartFileDetail{}, ErrChartFileNotFound
}

// chartFileEntries lists everything the archive holds, in a stable order.
//
// It reads `Raw`, which is every member of the archive as it was packaged —
// including the files under `charts/` that belong to subcharts. That is
// deliberate: installing a chart installs its subcharts, so a browser that
// hid them would be hiding most of what is about to be created.
//
// The second return value is the count before the bound was applied, so a
// truncated listing can say so.
func chartFileEntries(loaded *chart.Chart) ([]ChartFileEntry, int) {
	if loaded == nil {
		return nil, 0
	}
	files := make([]*chart.File, 0, len(loaded.Raw))
	for _, file := range loaded.Raw {
		if file == nil || file.Name == "" {
			continue
		}
		files = append(files, file)
	}
	sort.Slice(files, func(left, right int) bool {
		return files[left].Name < files[right].Name
	})
	total := len(files)
	if len(files) > maxChartFileEntries {
		files = files[:maxChartFileEntries]
	}
	entries := make([]ChartFileEntry, 0, len(files))
	for _, file := range files {
		entries = append(entries, ChartFileEntry{
			Path: file.Name,
			Size: len(file.Data),
			Text: isTextFile(file.Data),
		})
	}
	return entries, total
}

// isTextFile decides whether a chart member can be shown in a code viewer.
//
// A NUL byte or invalid UTF-8 in the head of the file is the test. It is not
// the file extension: a chart may package anything under any name, and a
// `.yaml` holding a gzip stream should not be handed to the browser as text.
func isTextFile(data []byte) bool {
	sample := data
	if len(sample) > textSampleBytes {
		sample = sample[:textSampleBytes]
		// The cut may land inside a multi-byte rune, which says nothing about
		// the file. Drop the trailing partial one before validating.
		for trimmed := 0; trimmed < 3 && !utf8.Valid(sample) && len(sample) > 0; trimmed++ {
			sample = sample[:len(sample)-1]
		}
	}
	return bytes.IndexByte(sample, 0) < 0 && utf8.Valid(sample)
}
