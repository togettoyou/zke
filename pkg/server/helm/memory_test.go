package helm

import (
	"testing"
	"time"

	"helm.sh/helm/v3/pkg/chart"
)

func parsedChart(name string, bytes int) *chart.Chart {
	return &chart.Chart{
		Metadata: &chart.Metadata{Name: name, Version: "1.0.0"},
		Raw: []*chart.File{
			{Name: "values.yaml", Data: make([]byte, bytes)},
		},
	}
}

// An expired entry leaves the cache when it is found, rather than sitting in it
// unreachable.
//
// Returning a miss and keeping the entry was a real leak: the chart could never
// be served again, but its bytes still counted against the budget, so the cache
// filled with entries it would refuse to hand out and evicted live ones to make
// room for them.
func TestExpiredChartLeavesTheCache(t *testing.T) {
	t.Parallel()

	cache := newChartCache()
	cache.put(testRepositoryID, "demo", "1.0.0", parsedChart("demo", 4096))
	if entries, bytes := cache.held(); entries != 1 || bytes == 0 {
		t.Fatalf("after put: %d entries, %d bytes", entries, bytes)
	}

	key := chartCacheKey(testRepositoryID, "demo", "1.0.0")
	cache.entries[key].fetchedAt = time.Now().Add(-chartCacheTTL - time.Second)

	if _, found := cache.get(testRepositoryID, "demo", "1.0.0"); found {
		t.Fatal("an expired chart was served")
	}
	entries, bytes := cache.held()
	if entries != 0 || bytes != 0 {
		t.Fatalf("an expired chart was kept: %d entries, %d bytes", entries, bytes)
	}
}

// Eviction spends the budget on entries that can still be served: an expired
// one can never be, so it goes before any live one does.
func TestChartEvictionDropsExpiredEntriesFirst(t *testing.T) {
	t.Parallel()

	cache := newChartCache()
	cache.put(testRepositoryID, "stale", "1.0.0", parsedChart("stale", 8192))
	staleKey := chartCacheKey(testRepositoryID, "stale", "1.0.0")
	cache.entries[staleKey].fetchedAt = time.Now().Add(-chartCacheTTL - time.Second)
	cache.put(testRepositoryID, "live", "1.0.0", parsedChart("live", 8192))

	if _, found := cache.entries[staleKey]; found {
		t.Fatal("an expired entry survived a later put")
	}
	if _, found := cache.get(testRepositoryID, "live", "1.0.0"); !found {
		t.Fatal("the live entry was dropped")
	}
	entries, bytes := cache.held()
	if entries != 1 || bytes != cache.entries[chartCacheKey(testRepositoryID, "live", "1.0.0")].size {
		t.Fatalf("accounting drifted: %d entries, %d bytes", entries, bytes)
	}
}

// Forgetting a repository takes its bytes with it. Without that the budget
// would shrink every time a repository was edited.
func TestForgettingARepositoryReleasesItsBytes(t *testing.T) {
	t.Parallel()

	cache := newChartCache()
	cache.put(testRepositoryID, "demo", "1.0.0", parsedChart("demo", 4096))
	cache.put(testRepositoryID, "demo", "2.0.0", parsedChart("demo", 4096))
	cache.forget(testRepositoryID)
	if entries, bytes := cache.held(); entries != 0 || bytes != 0 {
		t.Fatalf("after forget: %d entries, %d bytes", entries, bytes)
	}
}

// A parsed index is the largest thing this package holds, and the count bound
// alone never reclaims one on a platform with a handful of repositories. The
// idle bound does, and it costs a re-parse of the copy already on disk.
func TestIdleParsedIndexIsReleased(t *testing.T) {
	t.Parallel()

	cache := newIndexCache()
	now := time.Now()
	cache.entries["idle"] = &cachedIndex{usedAt: now.Add(-2 * time.Hour)}
	cache.entries["busy"] = &cachedIndex{usedAt: now.Add(-time.Minute)}

	cache.evictLocked(now, time.Hour)
	if _, found := cache.entries["idle"]; found {
		t.Fatal("an index nobody has read for a whole freshness window was kept")
	}
	if _, found := cache.entries["busy"]; !found {
		t.Fatal("an index in use was dropped")
	}
	if cache.held() != 1 {
		t.Fatalf("held = %d", cache.held())
	}
}

// A deployment with no freshness window configured must not have every index
// dropped on the next load; the count bound is what applies there.
func TestParsedIndexIdleBoundNeedsAWindow(t *testing.T) {
	t.Parallel()

	cache := newIndexCache()
	now := time.Now()
	cache.entries["one"] = &cachedIndex{usedAt: now.Add(-100 * time.Hour)}
	cache.evictLocked(now, 0)
	if cache.held() != 1 {
		t.Fatalf("held = %d, want the entry kept when no window is configured", cache.held())
	}
}
