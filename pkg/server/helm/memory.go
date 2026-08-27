package helm

import (
	"context"
	"sync"
	"time"

	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/repo"
)

// The layer in front of the disk cache.
//
// Both caches here hold *parsed* objects, which is the whole reason they exist
// alongside data/helm: the disk answers "do we have to go to the network", and
// these answer "do we have to parse it again". A public index is tens of
// megabytes of YAML and a chart is an archive to unpack; doing either on every
// keystroke in a search box would make a cache hit feel like a miss.
//
// Both are per process and never shared between replicas. Everything in them is
// derived from something authoritative, so a replica with a colder cache is
// slower and never wrong.

const (
	// How many repositories' parsed indexes are held at once. A platform with
	// more repositories than this still works; the least recently used index is
	// re-read from disk.
	maxCachedIndexes = 16
	// How long a parsed chart is reused. The archive underneath it does not
	// expire — a published version does not change — so this bounds memory
	// rather than staleness.
	chartCacheTTL = 5 * time.Minute
	// How much of one chart's unpacked contents may be held. A chart is
	// templates and defaults; one larger than this is served to the request
	// that fetched it and then dropped rather than kept.
	maxCachedChartBytes = 16 << 20
	// How much the whole parsed-chart cache may hold. A byte budget rather than
	// an entry count because charts differ by orders of magnitude in size, and
	// an entry count would bound the wrong thing.
	maxCachedChartsBytes = 32 << 20
)

// indexCache holds parsed repository indexes.
type indexCache struct {
	mutex   sync.Mutex
	entries map[string]*cachedIndex
	// inflight collapses concurrent reads of the same repository. Without it a
	// Console opening the chart browser fires one request per panel and each of
	// them parses — and possibly downloads — the whole index.
	inflight map[string]*sync.WaitGroup
}

type cachedIndex struct {
	index  *repo.IndexFile
	state  indexState
	usedAt time.Time
}

func newIndexCache() *indexCache {
	return &indexCache{
		entries:  make(map[string]*cachedIndex),
		inflight: make(map[string]*sync.WaitGroup),
	}
}

func (cache *indexCache) forget(repositoryID string) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	delete(cache.entries, repositoryID)
}

// index returns the parsed index, reading through to the disk cache and the
// repository only when this layer cannot answer.
//
// force is passed straight through: an operator asking to read the index again
// is asking about the repository, and answering out of memory would be the one
// response that cannot possibly be what they wanted.
func (cache *indexCache) index(
	ctx context.Context,
	service *Service,
	repositoryID string,
	force bool,
) (*repo.IndexFile, indexState, error) {
	if !isUUID(repositoryID) {
		return nil, indexState{}, ErrInvalidInput
	}
	for {
		cache.mutex.Lock()
		entry, found := cache.entries[repositoryID]
		// A stale entry — one served because the repository was unreachable —
		// is held only as long as a fresh one would be, and then tried again.
		// Holding it longer would turn one failed request into an outage that
		// outlives the repository's own.
		if found && !force && time.Since(entry.state.FetchedAt) < service.indexTTL {
			entry.usedAt = time.Now()
			index, state := entry.index, entry.state
			cache.mutex.Unlock()
			return index, state, nil
		}
		if waiter, inflight := cache.inflight[repositoryID]; inflight {
			cache.mutex.Unlock()
			// Someone else is reading. Wait for them rather than making the
			// same request, but never past this request's own deadline.
			done := make(chan struct{})
			go func() {
				waiter.Wait()
				close(done)
			}()
			select {
			case <-done:
				if force {
					// The read that just finished may have been the one this
					// caller wanted forced. Take its answer rather than making
					// a second unconditional request for the same document.
					force = false
				}
				continue
			case <-ctx.Done():
				return nil, indexState{}, ctx.Err()
			}
		}
		waiter := &sync.WaitGroup{}
		waiter.Add(1)
		cache.inflight[repositoryID] = waiter
		cache.mutex.Unlock()

		// The expired entry is handed down rather than dropped: if the
		// repository answers that nothing changed, the parse in hand is still
		// the answer, and re-deriving it from disk would make revalidation the
		// expensive path.
		var held *heldIndex
		if entry != nil && !force {
			held = &heldIndex{index: entry.index, fetchedAt: entry.state.FetchedAt}
		}

		index, state, err := service.loadIndex(ctx, repositoryID, force, held)

		cache.mutex.Lock()
		delete(cache.inflight, repositoryID)
		if err == nil {
			cache.entries[repositoryID] = &cachedIndex{
				index:  index,
				state:  state,
				usedAt: time.Now(),
			}
			cache.evictLocked()
		}
		cache.mutex.Unlock()
		waiter.Done()
		return index, state, err
	}
}

func (cache *indexCache) evictLocked() {
	for len(cache.entries) > maxCachedIndexes {
		oldestID := ""
		var oldest time.Time
		for id, entry := range cache.entries {
			if oldestID == "" || entry.usedAt.Before(oldest) {
				oldestID = id
				oldest = entry.usedAt
			}
		}
		delete(cache.entries, oldestID)
	}
}

// chartCache holds parsed chart archives.
//
// It exists because reading a chart is no longer one request. The file browser
// asks for one file at a time, and without this each of those would download
// the whole archive from the repository again — turning a reader clicking
// through templates into sustained traffic against somebody else's server.
//
// Like the index cache it is in memory and per process: an archive is derived
// data with an authoritative upstream, so a replica with a colder cache is
// slower and never wrong.
type chartCache struct {
	mutex   sync.Mutex
	entries map[string]*cachedChart
	// bytes is the sum of the entries' sizes, kept alongside them so eviction
	// does not have to walk the map to know whether it is done.
	bytes int
}

type cachedChart struct {
	repositoryID string
	chart        *chart.Chart
	size         int
	fetchedAt    time.Time
	usedAt       time.Time
}

func newChartCache() *chartCache {
	return &chartCache{entries: make(map[string]*cachedChart)}
}

// chartCacheKey keys on the resolved version. Callers resolve before they look,
// so "newest" and the number it resolved to land on one entry.
func chartCacheKey(repositoryID string, chartName string, version string) string {
	return repositoryID + "\x00" + chartName + "\x00" + version
}

func (cache *chartCache) get(
	repositoryID string,
	chartName string,
	version string,
) (*chart.Chart, bool) {
	if cache == nil {
		return nil, false
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, found := cache.entries[chartCacheKey(repositoryID, chartName, version)]
	if !found || time.Since(entry.fetchedAt) >= chartCacheTTL {
		return nil, false
	}
	entry.usedAt = time.Now()
	return entry.chart, true
}

func (cache *chartCache) put(
	repositoryID string,
	chartName string,
	version string,
	loaded *chart.Chart,
) {
	if cache == nil || loaded == nil {
		return
	}
	size := chartRawBytes(loaded)
	if size > maxCachedChartBytes {
		return
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	key := chartCacheKey(repositoryID, chartName, version)
	if previous, found := cache.entries[key]; found {
		cache.bytes -= previous.size
	}
	now := time.Now()
	cache.entries[key] = &cachedChart{
		repositoryID: repositoryID,
		chart:        loaded,
		size:         size,
		fetchedAt:    now,
		usedAt:       now,
	}
	cache.bytes += size
	cache.evictLocked()
}

// forget drops every chart read from one repository. A repository that was
// edited may point somewhere else or authenticate differently, so what it
// published a moment ago is no longer an answer to the same question.
func (cache *chartCache) forget(repositoryID string) {
	if cache == nil {
		return
	}
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	for key, entry := range cache.entries {
		if entry.repositoryID != repositoryID {
			continue
		}
		cache.bytes -= entry.size
		delete(cache.entries, key)
	}
}

func (cache *chartCache) evictLocked() {
	for cache.bytes > maxCachedChartsBytes && len(cache.entries) > 0 {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range cache.entries {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.usedAt
			}
		}
		cache.bytes -= cache.entries[oldestKey].size
		delete(cache.entries, oldestKey)
	}
}

// chartRawBytes is how much memory holding this chart costs, near enough to
// budget with: the archive's members are the bulk of it.
func chartRawBytes(loaded *chart.Chart) int {
	total := 0
	for _, file := range loaded.Raw {
		if file == nil {
			continue
		}
		total += len(file.Name) + len(file.Data)
	}
	return total
}
