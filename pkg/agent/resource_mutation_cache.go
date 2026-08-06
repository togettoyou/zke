package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"sync"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"google.golang.org/protobuf/proto"
)

const (
	defaultMutationReplayEntries = 2048
	defaultMutationReplayTTL     = 15 * time.Minute
)

type resourceMutationResult struct {
	response *agentv1.ResourceResponse
	body     []byte
	// applied reports whether this outcome may have changed cluster state, and
	// therefore has to keep its idempotency key reserved. A request the API
	// Server refused wrote nothing; a request that failed after Kubernetes may
	// already have committed — a 5xx, a timeout, a canceled call, or a response
	// too large to ship back — is applied as far as this Agent can tell.
	applied bool
}

type resourceMutationEntry struct {
	fingerprint [sha256.Size]byte
	ready       chan struct{}
	result      resourceMutationResult
	err         error
	expiresAt   time.Time
}

// resourceMutationCache suppresses duplicate mutation dispatches after a
// response is lost in transit. It belongs to the Agent process rather than one
// QUIC connection, so reconnecting does not reopen the normal retry window.
// Entries are bounded and expire; explicit names and Kubernetes preconditions
// remain the safety boundary across a full Agent restart.
//
// Only an outcome that may have changed cluster state keeps its key — see
// resourceMutationResult.applied. Recording a refusal would reserve the key for
// a request that never happened, and the operator's next attempt under it is
// normally the corrected one, which would come back as an IdempotencyConflict
// with nothing to correct it against.
type resourceMutationCache struct {
	mutex      sync.Mutex
	entries    map[string]*resourceMutationEntry
	maxEntries int
	ttl        time.Duration
}

func newResourceMutationCache() *resourceMutationCache {
	return &resourceMutationCache{
		entries:    make(map[string]*resourceMutationEntry),
		maxEntries: defaultMutationReplayEntries,
		ttl:        defaultMutationReplayTTL,
	}
}

func mutationFingerprint(
	request *agentv1.ResourceRequest,
	body []byte,
) ([sha256.Size]byte, error) {
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write(encoded)
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write(body)
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (cache *resourceMutationCache) do(
	ctx context.Context,
	key string,
	fingerprint [sha256.Size]byte,
	execute func() (resourceMutationResult, error),
) (resourceMutationResult, bool, error) {
	now := time.Now()
	cache.mutex.Lock()
	cache.removeExpiredLocked(now)
	if entry := cache.entries[key]; entry != nil {
		if entry.fingerprint != fingerprint {
			cache.mutex.Unlock()
			return resourceMutationResult{}, true, nil
		}
		ready := entry.ready
		cache.mutex.Unlock()
		select {
		case <-ready:
			cache.mutex.Lock()
			result := cloneMutationResult(entry.result)
			entryErr := entry.err
			cache.mutex.Unlock()
			return result, false, entryErr
		case <-ctx.Done():
			return resourceMutationResult{}, false, ctx.Err()
		}
	}
	cache.makeRoomLocked()
	entry := &resourceMutationEntry{
		fingerprint: fingerprint,
		ready:       make(chan struct{}),
	}
	cache.entries[key] = entry
	cache.mutex.Unlock()

	result, err := execute()
	cache.mutex.Lock()
	if err != nil {
		entry.err = err
		delete(cache.entries, key)
		close(entry.ready)
		cache.mutex.Unlock()
		return resourceMutationResult{}, false, err
	}
	entry.result = cloneMutationResult(result)
	entry.expiresAt = time.Now().Add(cache.ttl)
	// A refusal releases the key. The entry stays readable to callers already
	// waiting on it — they hold the pointer, and two dispatches of the same
	// request still collapse into one — but it leaves the map, so a later attempt
	// under that key executes instead of replaying an answer about a cluster that
	// has since changed, or being rejected for having been corrected.
	if !result.applied {
		delete(cache.entries, key)
	}
	close(entry.ready)
	cache.mutex.Unlock()
	return result, false, nil
}

func (cache *resourceMutationCache) removeExpiredLocked(now time.Time) {
	for key, entry := range cache.entries {
		if !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
			delete(cache.entries, key)
		}
	}
}

func (cache *resourceMutationCache) makeRoomLocked() {
	for len(cache.entries) >= cache.maxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range cache.entries {
			if entry.expiresAt.IsZero() {
				continue
			}
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey = key
				oldest = entry.expiresAt
			}
		}
		if oldestKey == "" {
			// All entries are in flight. The Resource Stream quota bounds this
			// temporary overflow, while dropping one would permit a duplicate.
			return
		}
		delete(cache.entries, oldestKey)
	}
}

func cloneMutationResult(result resourceMutationResult) resourceMutationResult {
	cloned := resourceMutationResult{
		body:    bytes.Clone(result.body),
		applied: result.applied,
	}
	if result.response != nil {
		cloned.response = proto.Clone(result.response).(*agentv1.ResourceResponse)
	}
	return cloned
}
