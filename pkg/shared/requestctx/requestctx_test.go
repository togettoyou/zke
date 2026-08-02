package requestctx

import (
	"context"
	"testing"
)

func TestRequestIDContext(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // ID intentionally promises nil-safe access.
	if found := ID(nil); found != "" {
		t.Fatalf("nil Context ID = %q", found)
	}
	parent := WithID(context.Background(), "request-a")
	if found := ID(parent); found != "request-a" {
		t.Fatalf("request ID = %q", found)
	}
	if found := ID(WithID(parent, "")); found != "request-a" {
		t.Fatalf("empty replacement changed request ID to %q", found)
	}
}
