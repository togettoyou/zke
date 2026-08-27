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
) ([]byte, bool) {
	if cache == nil || !isUUID(repositoryID) {
		return nil, false
	}
	path := cache.chartPath(repositoryID, chartName, version)
	metaBytes, err := os.ReadFile(path + ".json")
	if err != nil {
		return nil, false
	}
	var meta chartMeta
	if err := json.Unmarshal(metaBytes, &meta); err != nil {
		return nil, false
	}
	if expected := normalizeDigest(expectedDigest); expected != "" &&
		expected != normalizeDigest(meta.Digest) {
		// The published version is not the one on disk any more. Dropping it
		// here rather than leaving it to eviction keeps the next request from
		// making the same comparison and the same decision.
		cache.removeChart(repositoryID, path)
		return nil, false
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	if int64(len(body)) != meta.Size || sha256Hex(body) != normalizeDigest(meta.Digest) {
		cache.report(
			"cached Helm chart failed verification",
			repositoryID,
			fmt.Errorf("%s %s does not match its recorded digest", chartName, version),
		)
		cache.removeChart(repositoryID, path)
		return nil, false
	}
	// Reading counts as use. Eviction is by last use, and without this the
	// chart a platform installs most often would be the first one dropped.
	now := time.Now()
	_ = os.Chtimes(path, now, now)
	return body, true
}

// PutChart stores one chart archive.
func (cache *Cache) PutChart(repositoryID string, chartName string, version string, body []byte) {
	if cache == nil || !isUUID(repositoryID) || len(body) == 0 {
		return
	}
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	path := cache.chartPath(repositoryID, chartName, version)
	if err := os.MkdirAll(filepath.Dir(path), cacheDirectoryMode); err != nil {
		cache.report("create Helm chart cache directory", repositoryID, err)
		return
	}
	if err := writeFileAtomically(path, body); err != nil {
		cache.report("write cached Helm chart", repositoryID, err)
		return
	}
	cache.writeMeta(repositoryID, path+".json", chartMeta{
		Chart:     chartName,
		Version:   version,
		Digest:    "sha256:" + sha256Hex(body),
		Size:      int64(len(body)),
		FetchedAt: time.Now().UTC(),
	})
	cache.evict()
}

func (cache *Cache) removeChart(repositoryID string, path string) {
	mutex := cache.lock(repositoryID)
	mutex.Lock()
	defer mutex.Unlock()
	_ = os.Remove(path)
	_ = os.Remove(path + ".json")
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
// Called when an entry is deleted, and when it is edited: a repository that
// points somewhere else, or authenticates differently, published none of this.
// Leaving the files behind would keep an administrator who corrected a mistyped
// address looking at what the mistake returned, and would leave a deleted
// repository's charts on disk with nothing left to explain them.
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
	// The lock stays in the map. Dropping it here would hand a concurrent
	// writer a different mutex for the same repository, which is exactly the
	// moment the two must not be allowed to disagree; the map holds one small
	// entry per repository this process has ever touched.
}

// Prune removes cache directories for repositories that no longer exist.
//
// Deleting an entry cleans up after itself, but only while this Server is
// running: an entry removed against the database directly, or while the process
// was down, leaves a directory nobody will ever look at again. Reconciling once
// at startup is where that is noticed.
func (cache *Cache) Prune(known []string) {
	if cache == nil {
		return
	}
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

// evict drops the least recently used chart archives until the cache fits.
//
// By last use rather than by age: a chart that was published two years ago and
// is installed every day should outlive one pulled once last week. Indexes are
// not candidates — they are small, and they are the difference between a
// catalogue that degrades when a repository is unreachable and one that empties.
func (cache *Cache) evict() {
	if cache.maxBytes <= 0 {
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
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".tgz") {
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
	if err != nil || total <= cache.maxBytes {
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
		_ = os.Remove(candidate.path + ".json")
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
	temporary, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
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
