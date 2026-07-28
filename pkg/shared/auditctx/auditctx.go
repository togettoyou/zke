// Package auditctx bounds the writes that must outlive the request that caused
// them.
package auditctx

import (
	"context"
	"time"
)

// Detach returns a context for writing an audit record or a security counter,
// derived from parent for its values but not for its cancellation.
//
// An HTTP handler keeps running after the client disconnects; only the request
// context is cancelled. A write started on that context therefore fails the
// instant a caller hangs up — which for a refused operation means the audit
// trail loses exactly the event it exists to record, and for a failed login
// means the persistent lockout counter never advances. Neither may be at the
// discretion of the party being audited.
//
// The result is still bounded: cancellation is dropped, so timeout is the only
// thing that ends it, and callers must pass one. Values are preserved so that
// request-scoped logging attributes survive.
func Detach(
	parent context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), timeout)
}
