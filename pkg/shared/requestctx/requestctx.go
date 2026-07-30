// Package requestctx carries one request correlation identifier across HTTP,
// Server services and the Server-Agent protocol boundary.
package requestctx

import "context"

type requestIDKey struct{}

// WithID returns a child Context carrying id. Empty identifiers are ignored so
// callers never overwrite a valid upstream identifier with an absent value.
func WithID(ctx context.Context, id string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, requestIDKey{}, id)
}

// ID returns the correlation identifier carried by ctx.
func ID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(requestIDKey{}).(string)
	return value
}
