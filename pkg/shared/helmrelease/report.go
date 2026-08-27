// Package helmrelease is the JSON body a Helm Stream carries back from the
// Agent, and the bounds both sides apply to it.
//
// It is a leaf package holding nothing but the contract: the Agent writes this
// report after running Helm, the Server reads it and hands it to the Console,
// and neither side has to import the other's release types to agree on what a
// revision looks like. The read-only release views in the container service
// keep their own shape — they are built from the release Secret's labels and
// payload, not from a report — so the two do not have to move together.
package helmrelease

import "time"

const (
	// MaxChartBytes bounds the chart archive the Server may send. Charts are
	// small by construction — templates and defaults, not images — and a
	// deliberate multi-hundred-megabyte archive would be a way to make one
	// request cost an Agent its memory.
	MaxChartBytes uint64 = 16 << 20
	// MaxValuesBytes bounds the values document. Real values files are a few
	// kilobytes; this leaves room for a generated one without leaving room for
	// a payload sent to exhaust the renderer.
	MaxValuesBytes uint64 = 1 << 20
	// MaxManifestBytes bounds the rendered manifest carried back. A chart that
	// renders more than this is reported truncated rather than dropped: the
	// objects it created are readable one by one through the resource browser,
	// and a preview that silently ends early would be read as the whole change.
	MaxManifestBytes = 1 << 20
	// MaxNotesBytes bounds NOTES.txt, which is prose meant for a human and has
	// no reason to be larger than this.
	MaxNotesBytes = 128 << 10
	// MaxReportBytes bounds the whole JSON report. It sits above the manifest
	// and notes bounds with room for the rest of the fields, so a report that
	// respects those two cannot fail this one.
	MaxReportBytes uint64 = 4 << 20
	// MaxDescriptionLength bounds the free text recorded on the revision.
	MaxDescriptionLength = 256
	// MaxTimeoutSeconds bounds how long the Agent may wait for a release to
	// settle. It is well under any Agent request timeout the Server configures,
	// so the bound that actually applies is still the Stream's.
	MaxTimeoutSeconds = 3600
	// MaxHistoryLimit bounds how many revisions Helm is asked to retain.
	MaxHistoryLimit = 100
)

// Report is what the Agent knows about a release after Helm finished with it.
//
// A dry run produces one too: the fields describe what *would* be written, and
// Manifest carries the render an operator is being asked to approve. Nothing
// here is read back out of the Cluster afterwards — it is Helm's own account of
// what it just did.
type Report struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
	Revision  int64  `json:"revision"`
	Status    string `json:"status"`
	// DryRun says the report describes an unwritten change. It is carried in
	// the body rather than inferred from the request, so a Console that renders
	// a stored report cannot lose the distinction.
	DryRun           bool       `json:"dry_run"`
	Description      string     `json:"description"`
	ChartName        string     `json:"chart_name"`
	ChartVersion     string     `json:"chart_version"`
	AppVersion       string     `json:"app_version"`
	ChartDescription string     `json:"chart_description"`
	FirstDeployed    *time.Time `json:"first_deployed,omitempty"`
	LastDeployed     *time.Time `json:"last_deployed,omitempty"`
	// Notes is the chart's NOTES.txt as rendered for this release. It is the
	// one part of a release written to be read by whoever installed it.
	Notes string `json:"notes"`
	// Manifest is what the chart rendered. It is the whole point of a dry run
	// and is returned for a real run too, so the Console can show what was
	// applied without a second round trip.
	Manifest          string `json:"manifest"`
	ManifestTruncated bool   `json:"manifest_truncated"`
	NotesTruncated    bool   `json:"notes_truncated"`
	// Deleted is set by an uninstall. The rest of the report then describes the
	// revision that was removed, or, when history was kept, the uninstalled
	// revision Helm left behind.
	Deleted bool `json:"deleted"`
	// HooksDisabled records that the chart's hooks were skipped. A release
	// installed without its hooks is not the release the chart describes, so
	// the fact travels with the revision rather than only with the request.
	HooksDisabled bool `json:"hooks_disabled"`
}

// Truncate cuts the two unbounded fields to their limits and marks what it cut.
// It is applied by the Agent before the report is written, so the Server never
// has to decide whether an oversized body is a hostile Cluster or a large chart.
func (report *Report) Truncate() {
	if len(report.Manifest) > MaxManifestBytes {
		report.Manifest = report.Manifest[:MaxManifestBytes]
		report.ManifestTruncated = true
	}
	if len(report.Notes) > MaxNotesBytes {
		report.Notes = report.Notes[:MaxNotesBytes]
		report.NotesTruncated = true
	}
}
