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
	//
	// It is a weak bound on memory by itself, which is why it is not the only
	// one — see indexIdleFactor. A parsed public index is the largest object
	// this package ever holds: tens of megabytes of YAML become far more than
	// that as Go objects, and sixteen of them held for a repository nobody has
	// opened since boot is memory spent on nothing.
	maxCachedIndexes = 16
	// How long an unused parsed index is kept, as a multiple of the freshness
	// window the operator configured.
	//
	// An index that nobody has looked at for a whole freshness window is not in
	// use: anyone who wanted it in that time would have been served it and
	// refreshed its stamp. Dropping it costs a parse of the copy already on
	// disk — never a request to the repository — which is the cheapest thing
	// this package can be made to pay, and it is paid only by a repository that
	// had stopped being read.
	indexIdleFactor = 1
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

// held reports how many parsed indexes are in memory, for the tests that check
// the idle bound actually releases them.
func (cache *indexCache) held() int {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	return len(cache.entries)
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
		}
		// Swept on every load rather than only when the count is exceeded, and
		// swept whether or not this load succeeded: the indexes worth releasing
		// belong to repositories that are not being read, and a load of some
		// other repository is the only moment this cache is reliably awake.
		cache.evictLocked(time.Now(), service.indexTTL)
		cache.mutex.Unlock()
		waiter.Done()
		return index, state, err
	}
}

// evictLocked releases what is not being used, then what does not fit.
//
// The two are different questions and the idle pass answers the one that
// matters more here. A count bound only reclaims memory when a seventeenth
// repository is read, which on a platform with three repositories is never; the
// idle pass reclaims it because nobody is looking.
func (cache *indexCache) evictLocked(now time.Time, indexTTL time.Duration) {
	if indexTTL > 0 {
		idle := indexTTL * indexIdleFactor
		for id, entry := range cache.entries {
			if now.Sub(entry.usedAt) > idle {
				delete(cache.entries, id)
			}
		}
	}
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
	key := chartCacheKey(repositoryID, chartName, version)
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, found := cache.entries[key]
	if !found {
		return nil, false
	}
	if time.Since(entry.fetchedAt) >= chartCacheTTL {
		// Dropped here rather than left to be counted.
		//
		// Returning a miss and keeping the entry was the bug this replaces: an
		// expired chart could never be handed out again, but its bytes still
		// filled the budget, so the cache eventually held nothing but charts it
		// would refuse to serve — and evicted live entries to make room for
		// them.
		cache.dropLocked(key, entry)
		return nil, false
	}
	entry.usedAt = time.Now()
	return entry.chart, true
}

func (cache *chartCache) dropLocked(key string, entry *cachedChart) {
	cache.bytes -= entry.size
	delete(cache.entries, key)
}

// held reports how many parsed charts are in memory and how many bytes they are
// charged for, so a test can check the two have not drifted apart.
func (cache *chartCache) held() (int, int) {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	return len(cache.entries), cache.bytes
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
	now := time.Now()
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	key := chartCacheKey(repositoryID, chartName, version)
	if previous, found := cache.entries[key]; found {
		cache.bytes -= previous.size
	}
	cache.entries[key] = &cachedChart{
		repositoryID: repositoryID,
		chart:        loaded,
		size:         size,
		fetchedAt:    now,
		usedAt:       now,
	}
	cache.bytes += size
	cache.evictLocked(now)
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
		cache.dropLocked(key, entry)
	}
}

// evictLocked releases expired entries first and only then the least recently
// used ones.
//
// The order is the point. An expired entry can never be served again, so
// evicting a live one before it would be spending the budget on the entries
// least able to earn it back.
func (cache *chartCache) evictLocked(now time.Time) {
	for key, entry := range cache.entries {
		if now.Sub(entry.fetchedAt) >= chartCacheTTL {
			cache.dropLocked(key, entry)
		}
	}
	for cache.bytes > maxCachedChartsBytes && len(cache.entries) > 0 {
		oldestKey := ""
		var oldest time.Time
		for key, entry := range cache.entries {
			if oldestKey == "" || entry.usedAt.Before(oldest) {
				oldestKey = key
				oldest = entry.usedAt
			}
		}
		cache.dropLocked(oldestKey, cache.entries[oldestKey])
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
