package helm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// What chart repositories published, kept on this Server's disk.
//
// Before this existed, every catalogue page and every chart opened went to the
// repository. A process restart threw away everything, a slow repository made
// the Console slow, and an unreachable one made it empty — for charts that had
// been read minutes earlier and had not changed since.
//
// The layout follows Helm's own cache, because it is a layout operators already
// know how to read and clean out:
//
//	data/helm/<repository-id>/index.yaml         the index, verbatim
//	data/helm/<repository-id>/index.json         when it was read, and its validators
//	data/helm/<repository-id>/charts/<name>-<version>.tgz
//	data/helm/<repository-id>/charts/<name>-<version>.tgz.json
//
// Keyed by repository id rather than by name: a name can be edited, can collide
// between two entries once case is folded, and is chosen by a person. The id is
// stable for the life of the entry, which is exactly the life of what is cached
// under it.
//
// The two halves have different lifetimes on purpose. An index is a moving
// document and expires; a chart archive is one published version and does not
// change, so it is kept until the space is needed and validated by digest
// rather than by age.
type Cache struct {
	root   string
	logger *slog.Logger
	// maxBytes bounds the chart archives. Indexes are not counted and never
	// evicted: they are what makes a catalogue readable when the repository is
	// not, and they are small next to the archives.
	maxBytes int64
	// writes serialises writes per repository. Two requests for the same chart
	// arrive together often — a page that opens a chart also lists its files —
	// and without this they race to rename over each other's temporary file.
	writes sync.Map
	// stored is what eviction knows about how full the cache is without going
	// and looking. See evict.
	stored storedBytes
}

// storedBytes is the running size of the cached archives.
//
// Eviction has to know the total, and the only way to learn it is to walk the
// tree — thousands of stat calls on a cache that is holding thousands of charts.
// Doing that after every download made the walk part of the cost of installing
// anything, for a question whose answer is almost always "there is plenty of
// room".
//
// So the total is remembered, and the walk happens only when the remembered
// value says it might matter. The error can only ever run high: bytes are added
// here when a chart is stored, and every path that removes them either
// subtracts or gives up on the total entirely. A high estimate costs one walk
// that corrects it; a low one would miss an eviction, and no path produces one.
type storedBytes struct {
	mutex sync.Mutex
	// known is false until a walk has established the total — at startup, and
	// again after a removal whose size this cannot account for.
	known bool
	bytes int64
}

func (stored *storedBytes) add(size int64) {
	stored.mutex.Lock()
	stored.bytes += size
	stored.mutex.Unlock()
}

func (stored *storedBytes) sub(size int64) {
	stored.mutex.Lock()
	stored.bytes -= size
	if stored.bytes < 0 {
		stored.bytes = 0
	}
	stored.mutex.Unlock()
}

// forget gives up on the total. Called where what was removed cannot be
// measured — a whole repository directory, say — so that the next check walks
// rather than trusting a number that is now wrong in the dangerous direction.
func (stored *storedBytes) forget() {
	stored.mutex.Lock()
	stored.known = false
	stored.mutex.Unlock()
}

func (stored *storedBytes) set(total int64) {
	stored.mutex.Lock()
	stored.bytes = total
	stored.known = true
	stored.mutex.Unlock()
}

// fits reports whether the cache is known to be within the bound, and therefore
// whether the walk can be skipped.
func (stored *storedBytes) fits(maxBytes int64) bool {
	stored.mutex.Lock()
	defer stored.mutex.Unlock()
	return stored.known && stored.bytes <= maxBytes
}

// IndexMeta is what this Server knows about a cached index besides its body.
//
// The validators are the point of storing anything alongside it: with an ETag,
// an expired index costs a conditional request and a 304 rather than another
// download of a document that runs to tens of megabytes.
type IndexMeta struct {
	// URL is the address the body came from. A repository that was pointed
	// somewhere else has a cached body that answers a different question, and
	// comparing here catches that even if nothing invalidated the entry.
	URL          string    `json:"url"`
	FetchedAt    time.Time `json:"fetched_at"`
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
}

type chartMeta struct {
	Chart     string    `json:"chart"`
	Version   string    `json:"version"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	FetchedAt time.Time `json:"fetched_at"`
	// ProvenanceChecked says the fetch that stored this archive also asked the
	// repository for the signature beside it. Without it, an archive with no
	// `.prov` on disk is ambiguous — the repository may publish none, or this
	// entry may predate the Server that would have asked — and the two have
	// opposite meanings under a policy that requires one. An entry that was
	// never asked is treated as a miss the first time a policy needs the answer.
	//
	// It does not have to survive a policy change: changing a repository's
	// signing policy or its keyring forgets everything cached under it — see
	// repositorySourceChanged — so every archive here was fetched under the
	// policy in force now.
	ProvenanceChecked bool `json:"provenance_checked"`
}

// CachedChart is one archive together with what was published beside it, in
// both directions: it is what a read returns and what a write is given.
//
// The two travel as a pair because verifying one without the other is not
// possible — a provenance file signs a digest, and the digest is of these bytes.
type CachedChart struct {
	Archive []byte
	// Provenance is the `.prov` document, empty when the repository publishes
	// none. ProvenanceChecked separates that from never having asked.
	Provenance        []byte
	ProvenanceChecked bool
}

const (
	// The cache holds what a repository published, which includes credentials
	// in no form at all — but it also reveals which charts a platform runs, and
	// it sits next to data/pki. It gets the same permissions.
	cacheDirectoryMode os.FileMode = 0o700
	cacheFileMode      os.FileMode = 0o600
	// The longest sanitised chart name kept in a filename. Past this the hash
	// suffix is doing the identifying anyway, and file name limits are real.
	maxCacheNameLength = 96
	// The extension Helm gives a chart's detached signature. Kept beside the
	// archive under the same name, as `helm pull --prov` writes it, so an
	// operator reading the directory sees the pair the way they expect to.
	provenanceSuffix = ".prov"
	// The suffix on an archive's metadata sidecar.
	metadataSuffix = ".json"
	// The extension the cached archives carry, and what eviction counts.
	archiveSuffix = ".tgz"
	// What a half-finished write is called. It is a fixed prefix rather than
	// whatever CreateTemp likes because the startup sweep has to be able to
	// recognise one, and a name it merely guessed at would be a name it could
	// delete something else by.
	temporaryFilePrefix = ".tmp-"
	// How old a temporary file has to be before it is assumed abandoned.
	//
	// One only exists between a create and a rename — microseconds — so an hour
	// is not a guess at how long a write takes, it is a margin wide enough that
	// no live write can be inside it. The startup sweep is the only caller and
	// nothing is writing yet when it runs; the margin is there so that a second
	// Server sharing the directory could not have one deleted underneath it.
	strayTemporaryAge = time.Hour
)

// NewCache prepares the cache directory. An empty directory disables caching
// entirely: every method on a nil *Cache is a miss and a no-op, so the rest of
// the package does not branch on whether one is configured.
func NewCache(directory string, maxBytes int64, logger *slog.Logger) (*Cache, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	root, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve Helm cache directory: %w", err)
	}
	if err := os.MkdirAll(root, cacheDirectoryMode); err != nil {
		return nil, fmt.Errorf("create Helm cache directory: %w", err)
	}
	return &Cache{root: root, logger: logger, maxBytes: maxBytes}, nil
}

// Directory reports where the cache lives, for logging and for tests.
func (cache *Cache) Directory() string {
	if cache == nil {
		return ""
	}
	return cache.root
}

func (cache *Cache) repositoryDirectory(repositoryID string) string {
	return filepath.Join(cache.root, repositoryID)
}

func (cache *Cache) lock(repositoryID string) *sync.Mutex {
	value, _ := cache.writes.LoadOrStore(repositoryID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// Index returns the cached index body and what is known about it.
//
// The body is returned even when it is old. Whether an index that was read
// three days ago is still an answer is a decision about this request, not about
// the file, so it is made by the caller with FetchedAt in hand.
func (cache *Cache) Index(repositoryID string) ([]byte, IndexMeta, bool) {
	if cache == nil || !isUUID(repositoryID) {
		return nil, IndexMeta{}, false
	}
	directory := cache.repositoryDirectory(repositoryID)
	body, err := os.ReadFile(filepath.Join(directory, "index.yaml"))
	if err != nil || len(body) == 0 {
		return nil, IndexMeta{}, false
	}
	metaBytes, err := os.ReadFile(filepath.Join(directory, "index.json"))
	if err != nil {
		// A body with no metadata is unusable rather than merely undated: with
		// no URL to compare and no time to age it against, there is no question
		// it can answer.
		return nil, IndexMeta{}, false
	}
	var meta IndexMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, IndexMeta{}, false
	}
	return body, meta, true
}

// PutIndex stores an index body and its validators.
func (cache *Cache) PutIndex(repositoryID string, body []byte, meta IndexMeta) {
	if cache == nil || !isUUID(repositoryID) || len(body) == 0 {
		return
	}
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	directory := cache.repositoryDirectory(repositoryID)
	if err := os.MkdirAll(directory, cacheDirectoryMode); err != nil {
		cache.report("create Helm cache repository directory", repositoryID, err)
		return
	}
	if err := writeFileAtomically(filepath.Join(directory, "index.yaml"), body); err != nil {
		cache.report("write cached Helm index", repositoryID, err)
		return
	}
	cache.writeMeta(repositoryID, filepath.Join(directory, "index.json"), meta)
}

// TouchIndex records that the repository confirmed the cached body is current.
//
// This is what a 304 means, and writing the time back is what makes it worth
// asking: without it the next request revalidates again, and a repository that
// never changes is asked about on every TTL boundary forever.
func (cache *Cache) TouchIndex(repositoryID string, meta IndexMeta) {
	if cache == nil || !isUUID(repositoryID) {
		return
	}
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	cache.writeMeta(
		repositoryID,
		filepath.Join(cache.repositoryDirectory(repositoryID), "index.json"),
		meta,
	)
}

func (cache *Cache) writeMeta(repositoryID string, path string, meta any) {
	encoded, err := json.Marshal(meta)
	if err != nil {
		cache.report("encode Helm cache metadata", repositoryID, err)
		return
	}
	if err := writeFileAtomically(path, encoded); err != nil {
		cache.report("write Helm cache metadata", repositoryID, err)
	}
}

// Chart returns a cached chart archive.
//
// The bytes are verified before they are handed back. Two different questions
// are being answered: the sidecar digest says whether this file is still what
// was written here, and expectedDigest — the digest the repository's own index
// publishes for this version, when it publishes one — says whether what was
// written here is still what the repository serves. A version that was
// republished under the same number fails the second and is re-fetched.
func (cache *Cache) Chart(
	repositoryID string,
	chartName string,
	version string,
	expectedDigest string,
) (CachedChart, bool) {
	if cache == nil || !isUUID(repositoryID) {
		return CachedChart{}, false
	}
	path := cache.chartPath(repositoryID, chartName, version)
	metaBytes, err := os.ReadFile(path + metadataSuffix)
	if err != nil {
		return CachedChart{}, false
	}
	var meta chartMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return CachedChart{}, false
	}
	if expected := normalizeDigest(expectedDigest); expected != "" &&
		expected != normalizeDigest(meta.Digest) {
		// The published version is not the one on disk any more. Dropping it
		// here rather than leaving it to eviction keeps the next request from
		// making the same comparison and the same decision.
		cache.removeChart(repositoryID, path)
		return CachedChart{}, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return CachedChart{}, false
	}
	if int64(len(body)) != meta.Size || sha256Hex(body) != normalizeDigest(meta.Digest) {
		cache.report(
			"cached Helm chart failed verification",
			repositoryID,
			fmt.Errorf("%s %s does not match its recorded digest", chartName, version),
		)
		cache.removeChart(repositoryID, path)
		return CachedChart{}, false
	}
	// A missing file is not an error: it is what a repository that publishes no
	// signature leaves behind, and ProvenanceChecked is what says whether that
	// absence was observed or merely never looked for.
	provenance, _ := os.ReadFile(path + provenanceSuffix)
	// Reading counts as use. Eviction is by last use, and without this the
	// chart a platform installs most often would be the first one dropped.
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return CachedChart{
		Archive:           body,
		Provenance:        provenance,
		ProvenanceChecked: meta.ProvenanceChecked,
	}, true
}

// PutChart stores one chart archive and the signature published beside it.
//
// The two are written together because they are only meaningful together, and
// an empty provenance removes whatever was there rather than leaving it: a
// document that signs a digest is wrong the moment the bytes next to it change.
func (cache *Cache) PutChart(
	repositoryID string,
	chartName string,
	version string,
	chart CachedChart,
) {
	if cache == nil || !isUUID(repositoryID) || len(chart.Archive) == 0 {
		return
	}
	if !cache.storeChart(repositoryID, chartName, version, chart) {
		return
	}
	cache.stored.add(int64(len(chart.Archive)))
	// Outside the repository's lock on purpose. Eviction walks the whole cache,
	// which is a directory tree, and holding a per-repository write lock across
	// it would make every other write to that repository wait behind a walk of
	// everybody else's files.
	cache.evict()
}

// storeChart performs the write itself and reports whether anything landed.
func (cache *Cache) storeChart(
	repositoryID string,
	chartName string,
	version string,
	chart CachedChart,
) bool {
	body := chart.Archive
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	path := cache.chartPath(repositoryID, chartName, version)
	if err := os.MkdirAll(filepath.Dir(path), cacheDirectoryMode); err != nil {
		cache.report("create Helm chart cache directory", repositoryID, err)
		return false
	}
	if err := writeFileAtomically(path, body); err != nil {
		cache.report("write cached Helm chart", repositoryID, err)
		return false
	}
	if len(chart.Provenance) > 0 {
		if err := writeFileAtomically(path+provenanceSuffix, chart.Provenance); err != nil {
			cache.report("write cached Helm chart provenance", repositoryID, err)
		}
	} else {
		_ = os.Remove(path + provenanceSuffix)
	}
	cache.writeMeta(repositoryID, path+metadataSuffix, chartMeta{
		Chart:             chartName,
		Version:           version,
		Digest:            "sha256:" + sha256Hex(body),
		Size:              int64(len(body)),
		FetchedAt:         time.Now().UTC(),
		ProvenanceChecked: chart.ProvenanceChecked,
	})
	return true
}

func (cache *Cache) removeChart(repositoryID string, path string) {
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	cache.stored.sub(removeChartFiles(path))
}

// removeChartFiles drops an archive and everything stored beside it, and
// reports how many bytes of archive went. The three files are only meaningful
// together, so they are always removed together — a sidecar left behind
// describes a file that is not there any more.
func removeChartFiles(path string) int64 {
	var size int64
	if info, err := os.Stat(path); err == nil {
		size = info.Size()
	}
	if err := os.Remove(path); err != nil {
		size = 0
	}
	_ = os.Remove(path + metadataSuffix)
	_ = os.Remove(path + provenanceSuffix)
	return size
}

// chartPath names the file one chart version is stored in.
//
// Readable, because the point of following Helm's layout is that an operator
// can look in the directory and recognise what is there — and hashed, because
// a chart name comes out of a repository's index and is therefore not this
// Server's to trust as a path component. The sanitised name is for the reader
// and the hash is what actually identifies the file.
func (cache *Cache) chartPath(repositoryID string, chartName string, version string) string {
	sum := sha256.Sum256([]byte(chartName + "\x00" + version))
	name := fmt.Sprintf(
		"%s-%s-%s.tgz",
		sanitizeCacheComponent(chartName),
		sanitizeCacheComponent(version),
		hex.EncodeToString(sum[:6]),
	)
	return filepath.Join(cache.repositoryDirectory(repositoryID), "charts", name)
}

// Forget removes everything cached for one repository.
//
// Called when an entry is deleted, and when an edit changed where it is read
// from or on what terms: a repository that points somewhere else, or
// authenticates differently, published none of this. Leaving the files behind
// would keep an administrator who corrected a mistyped address looking at what
// the mistake returned, and would leave a deleted repository's charts on disk
// with nothing left to explain them.
func (cache *Cache) Forget(repositoryID string) {
	if cache == nil || !isUUID(repositoryID) {
		return
	}
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	if err := os.RemoveAll(cache.repositoryDirectory(repositoryID)); err != nil {
		cache.report("remove Helm cache directory", repositoryID, err)
	}
	// A whole directory went and there is no telling how much of it was
	// archives, so the running total stops being trusted rather than becoming
	// quietly wrong.
	cache.stored.forget()
	// The lock stays in the map. Dropping it here would hand a concurrent
	// writer a different mutex for the same repository, which is exactly the
	// moment the two must not be allowed to disagree; the map holds one small
	// entry per repository this process has ever touched.
}

// Prune brings the cache directory back into a state the rest of this file
// assumes it is in.
//
// It runs once at startup, and startup is exactly when it is needed: everything
// it cleans up is the residue of a process that stopped without finishing, or
// of a change made while no process was running.
//
//   - Directories for repositories that no longer exist. Deleting an entry
//     cleans up after itself while this Server is running; one removed against
//     the database directly, or while the process was down, leaves a directory
//     nobody will ever look at again.
//   - Half-finished writes. A chart archive is written to a temporary file and
//     renamed, so a process killed between the two leaves a full-sized file
//     that nothing else here can see: it is not an archive, so eviction neither
//     counts nor removes it, and it is not a repository directory, so the
//     reconcile above never reaches it. Left alone it is a permanent leak of
//     exactly the largest thing the cache stores.
//   - Sidecars whose archive is gone. Small, but they describe nothing and
//     would never be looked at again.
//
// It ends by applying the size bound, which is otherwise only applied when a
// chart is stored — so a cache that grew while `max_bytes` was larger, or that
// was over the bound when the process died, is brought back under it now rather
// than at the next download.
func (cache *Cache) Prune(known []string) {
	if cache == nil {
		return
	}
	cache.pruneRepositories(known)
	cache.sweepStrays(time.Now())
	cache.evict()
}

func (cache *Cache) pruneRepositories(known []string) {
	live := make(map[string]struct{}, len(known))
	for _, id := range known {
		live[id] = struct{}{}
	}
	entries, err := os.ReadDir(cache.root)
	if err != nil {
		cache.report("read Helm cache directory", "", err)
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() || !isUUID(entry.Name()) {
			continue
		}
		if _, found := live[entry.Name()]; found {
			continue
		}
		cache.logger.Info(
			"removing Helm cache for a repository that no longer exists",
			slog.String("repository_id", entry.Name()),
		)
		cache.Forget(entry.Name())
	}
}

// sweepStrays removes what no read and no eviction can reach.
//
// A walk error on one subtree is never a reason to abandon the sweep; the rest
// of the tree still has files nothing else will ever remove.
func (cache *Cache) sweepStrays(now time.Time) {
	var temporaryFiles, orphanedSidecars int
	_ = filepath.WalkDir(cache.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return nil //nolint:nilerr // see above
		}
		name := entry.Name()
		switch {
		case strings.HasPrefix(name, temporaryFilePrefix):
			info, statErr := entry.Info()
			// Age is what separates abandoned from in flight. Without a
			// readable timestamp there is no way to tell them apart, so the
			// file is left where it is.
			if statErr != nil || now.Sub(info.ModTime()) <= strayTemporaryAge {
				return nil
			}
			if os.Remove(path) == nil {
				temporaryFiles++
			}
		case strings.HasSuffix(name, archiveSuffix+metadataSuffix),
			strings.HasSuffix(name, archiveSuffix+provenanceSuffix):
			// Matched on the archive's own extension as well as the sidecar's,
			// so that index.json — which is not a sidecar and is not derived
			// from an archive — cannot be mistaken for one.
			archive := path[:strings.LastIndex(path, archiveSuffix)+len(archiveSuffix)]
			if _, statErr := os.Stat(archive); statErr == nil {
				return nil
			}
			if os.Remove(path) == nil {
				orphanedSidecars++
			}
		}
		return nil
	})
	if temporaryFiles == 0 && orphanedSidecars == 0 {
		return
	}
	cache.logger.Info(
		"swept the Helm chart cache",
		slog.Int("abandoned_writes", temporaryFiles),
		slog.Int("orphaned_sidecars", orphanedSidecars),
	)
}

// evict drops the least recently used chart archives until the cache fits.
//
// By last use rather than by age: a chart that was published two years ago and
// is installed every day should outlive one pulled once last week. Indexes are
// not candidates — they are small, and they are the difference between a
// catalogue that degrades when a repository is unreachable and one that empties.
//
// Answering "does it fit" is the expensive half, because it means walking the
// tree. On a cache that is nowhere near its bound — which is the ordinary state
// of one — that walk is pure cost on the path that installs a chart, so it is
// skipped whenever the running total already says there is room. See
// storedBytes for why that total can only err in the harmless direction.
func (cache *Cache) evict() {
	if cache.maxBytes <= 0 || cache.stored.fits(cache.maxBytes) {
		return
	}
	type archive struct {
		path   string
		size   int64
		usedAt time.Time
	}
	var archives []archive
	var total int64
	err := filepath.WalkDir(cache.root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, archiveSuffix) {
			// A walk error on one subtree is not a reason to abandon the sweep:
			// the rest of the cache still needs to be brought under the bound.
			return nil //nolint:nilerr // see above
		}
		info, statErr := entry.Info()
		if statErr != nil {
			return nil
		}
		archives = append(archives, archive{path, info.Size(), info.ModTime()})
		total += info.Size()
		return nil
	})
	if err != nil {
		return
	}
	// Whatever the walk found is the truth, so record it whether or not
	// anything has to go: the point of walking was to stop having to.
	defer func() { cache.stored.set(total) }()
	if total <= cache.maxBytes {
		return
	}
	sort.Slice(archives, func(left, right int) bool {
		return archives[left].usedAt.Before(archives[right].usedAt)
	})
	for _, candidate := range archives {
		if total <= cache.maxBytes {
			return
		}
		if removeErr := os.Remove(candidate.path); removeErr != nil {
			continue
		}
		_ = os.Remove(candidate.path + metadataSuffix)
		_ = os.Remove(candidate.path + provenanceSuffix)
		total -= candidate.size
	}
}

func (cache *Cache) report(message string, repositoryID string, err error) {
	if cache == nil || cache.logger == nil {
		return
	}
	attributes := []any{slog.String("error", err.Error())}
	if repositoryID != "" {
		attributes = append(attributes, slog.String("repository_id", repositoryID))
	}
	// A cache failure is never a request failure: the repository is still
	// reachable and the answer is still correct, it just cost what it used to
	// cost. It is logged so a full or unwritable volume is visible.
	cache.logger.Warn(message, attributes...)
}

// writeFileAtomically leaves either the previous contents or the new ones, never
// a half-written file. A reader that found one would verify its digest, discard
// it and fetch again — correct, but the fix is cheaper than the diagnosis.
func writeFileAtomically(path string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), temporaryFilePrefix+"*")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, cacheFileMode); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func sha256Hex(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// normalizeDigest reduces the forms an index writes a digest in to one. Helm's
// own index files carry a bare hex string; OCI and most other tooling prefix it
// with the algorithm.
func normalizeDigest(value string) string {
	trimmed := strings.ToLower(strings.TrimSpace(value))
	trimmed = strings.TrimPrefix(trimmed, "sha256:")
	if len(trimmed) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(trimmed); err != nil {
		return ""
	}
	return trimmed
}

// sanitizeCacheComponent keeps a name readable in a directory listing without
// letting it be anything but a name. Everything outside the set becomes `_`, so
// a separator, a traversal or an absolute path cannot survive it.
func sanitizeCacheComponent(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '.', character == '-', character == '_':
			builder.WriteRune(character)
		default:
			builder.WriteByte('_')
		}
		if builder.Len() >= maxCacheNameLength {
			break
		}
	}
	sanitized := strings.Trim(builder.String(), ".")
	if sanitized == "" {
		return "chart"
	}
	return sanitized
}
