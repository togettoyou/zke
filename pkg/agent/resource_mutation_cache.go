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

// mutationReplayResult is one recorded outcome. It is generic over the response
// message because two Streams need the same mechanism and carry different
// messages: a single-object write answers with a ResourceResponse, a Helm
// release change with a HelmResponse.
type mutationReplayResult[T proto.Message] struct {
	response T
	body     []byte
	// applied reports whether this outcome may have changed cluster state, and
	// therefore has to keep its idempotency key reserved. A request the API
	// Server refused wrote nothing; a request that failed after Kubernetes may
	// already have committed — a 5xx, a timeout, a canceled call, or a response
	// too large to ship back — is applied as far as this Agent can tell.
	applied bool
}

type resourceMutationResult = mutationReplayResult[*agentv1.ResourceResponse]

type mutationReplayEntry[T proto.Message] struct {
	fingerprint [sha256.Size]byte
	ready       chan struct{}
	result      mutationReplayResult[T]
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
type mutationReplayCache[T proto.Message] struct {
	mutex      sync.Mutex
	entries    map[string]*mutationReplayEntry[T]
	maxEntries int
	ttl        time.Duration
}

type resourceMutationCache = mutationReplayCache[*agentv1.ResourceResponse]

func newMutationReplayCache[T proto.Message]() *mutationReplayCache[T] {
	return &mutationReplayCache[T]{
		entries:    make(map[string]*mutationReplayEntry[T]),
		maxEntries: defaultMutationReplayEntries,
		ttl:        defaultMutationReplayTTL,
	}
}

func newResourceMutationCache() *resourceMutationCache {
	return newMutationReplayCache[*agentv1.ResourceResponse]()
}

// mutationFingerprint identifies a request by everything it asks for, so a key
// reused for a *different* request is a conflict rather than a replay. The
// variadic parts are the request bodies, which are not part of the message.
func mutationFingerprint(
	request proto.Message,
	parts ...[]byte,
) ([sha256.Size]byte, error) {
	encoded, err := (proto.MarshalOptions{Deterministic: true}).Marshal(request)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hash := sha256.New()
	_, _ = hash.Write(encoded)
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(part)
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func (cache *mutationReplayCache[T]) do(
	ctx context.Context,
	key string,
	fingerprint [sha256.Size]byte,
	execute func() (mutationReplayResult[T], error),
) (mutationReplayResult[T], bool, error) {
	now := time.Now()
	cache.mutex.Lock()
	cache.removeExpiredLocked(now)
	if entry := cache.entries[key]; entry != nil {
		if entry.fingerprint != fingerprint {
			cache.mutex.Unlock()
			return mutationReplayResult[T]{}, true, nil
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
			return mutationReplayResult[T]{}, false, ctx.Err()
		}
	}
	cache.makeRoomLocked()
	entry := &mutationReplayEntry[T]{
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
		return mutationReplayResult[T]{}, false, err
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

func (cache *mutationReplayCache[T]) removeExpiredLocked(now time.Time) {
	for key, entry := range cache.entries {
		if !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
			delete(cache.entries, key)
		}
	}
}

func (cache *mutationReplayCache[T]) makeRoomLocked() {
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

func cloneMutationResult[T proto.Message](
	result mutationReplayResult[T],
) mutationReplayResult[T] {
	cloned := mutationReplayResult[T]{
		body:    bytes.Clone(result.body),
		applied: result.applied,
	}
	// A typed nil pointer is not `== nil` through an interface, and reflecting
	// on it is what tells the two apart. An entry recorded without a response
	// must not be cloned into an empty message that reads as one.
	if any(result.response) != nil && result.response.ProtoReflect().IsValid() {
		cloned.response, _ = proto.Clone(result.response).(T)
	}
	return cloned
}
