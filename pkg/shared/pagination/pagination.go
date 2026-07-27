// Package pagination defines the single paging contract shared by every ZKE
// list endpoint. ZKE uses offset paging rather than cursors so that the
// Console can navigate by page number and show a stable total for a filtered
// list; the price is that a very large offset costs the database a scan, which
// is why Offset is capped.
package pagination

import "errors"

const (
	// DefaultLimit applies when a request omits the page size.
	DefaultLimit = 50
	// MaxLimit caps a single page so one request cannot pull a whole table.
	MaxLimit = 100
	// MaxOffset caps how deep a client may page, bounding the scan cost that
	// offset paging imposes on the database.
	MaxOffset = 1_000_000
)

// ErrInvalidPage reports a page request outside the supported bounds.
var ErrInvalidPage = errors.New("invalid pagination request")

// Request is one page of a filtered list, expressed as a size and a starting
// offset. The Console derives it from a page size and a page number.
type Request struct {
	Limit  int
	Offset int
}

// Validate reports whether the request is within the supported bounds.
func (request Request) Validate() error {
	if request.Limit < 1 || request.Limit > MaxLimit {
		return ErrInvalidPage
	}
	if request.Offset < 0 || request.Offset > MaxOffset {
		return ErrInvalidPage
	}
	return nil
}

// Result reports where a returned page sits inside the full filtered set.
// Total counts every row matching the filter, not just the returned page, so
// it stays correct even when the requested offset lies beyond the end.
type Result struct {
	Limit   int
	Offset  int
	Total   int
	HasMore bool
}

// NewResult describes a page of returned items inside a filtered total.
func NewResult(request Request, total int, returned int) Result {
	return Result{
		Limit:   request.Limit,
		Offset:  request.Offset,
		Total:   total,
		HasMore: request.Offset+returned < total,
	}
}
