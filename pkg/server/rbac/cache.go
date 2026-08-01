package rbac

import (
	"context"
	"sync"

	"github.com/togettoyou/zke/pkg/server/store"
)

type bindingCacheKey struct{}

// bindingCache memoizes RoleBinding lookups for the lifetime of a single
// request. Authorization middleware and the service behind it both resolve the
// caller's bindings, so without memoization one request can pay the same query
// two or three times as endpoints compose.
//
// The cache deliberately has no expiry, which makes where it is installed part
// of the authorization contract: it must be scoped to one short request. A
// long-lived stream must not install it, otherwise a RoleBinding withdrawn
// mid-stream would keep being honoured until the client reconnects.
type bindingCache struct {
	mutex    sync.Mutex
	bindings map[string][]store.RoleBinding
}

// WithBindingCache returns a context that memoizes RoleBinding lookups for the
// work derived from it.
func WithBindingCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, bindingCacheKey{}, &bindingCache{
		bindings: make(map[string][]store.RoleBinding),
	})
}

// WithoutBindingCache prevents a cache inherited from an HTTP request from
// being reused by a later authorization check. Long-lived streams use it when
// periodically revalidating access so a withdrawn RoleBinding takes effect.
func WithoutBindingCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, bindingCacheKey{}, (*bindingCache)(nil))
}

// listRoleBindings resolves a subject's bindings, reusing the request-scoped
// memo when one is installed.
func (service *Service) listRoleBindings(
	ctx context.Context,
	userID string,
) ([]store.RoleBinding, error) {
	cache, _ := ctx.Value(bindingCacheKey{}).(*bindingCache)
	if cache == nil {
		return service.store.ListRoleBindings(ctx, userID)
	}

	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	if bindings, cached := cache.bindings[userID]; cached {
		return bindings, nil
	}
	bindings, err := service.store.ListRoleBindings(ctx, userID)
	if err != nil {
		// A failed lookup is never memoized: the next attempt inside this
		// request must be free to reach the database again.
		return nil, err
	}
	cache.bindings[userID] = bindings
	return bindings, nil
}
