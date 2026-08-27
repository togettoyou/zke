package helm

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func indexReads(server *repositoryServer) int {
	count := 0
	for _, path := range server.requests {
		if path == "/index.yaml" {
			count++
		}
	}
	return count
}

func cachedFiles(t *testing.T, directory string, suffix string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, suffix) {
			return nil //nolint:nilerr // a partial walk still answers the question
		}
		found = append(found, path)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}

// The whole point: a restarted Server does not go back to the repository for
// what it already read.
func TestCacheSurvivesARestart(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)

	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}
	if indexReads(server) != 1 || chartDownloads(server) != 1 {
		t.Fatalf("first read made %v", server.requests)
	}

	// A second Service over the same directory is what a restart looks like:
	// every in-memory cache is empty and only the disk carries anything over.
	restarted, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	detail, err := restarted.GetChart(context.Background(), testRepositoryID, "demo", "")
	if err != nil {
		t.Fatalf("GetChart() after restart = %v", err)
	}
	if !strings.Contains(detail.Values, "# how many copies to run") {
		t.Fatalf("values = %q", detail.Values)
	}
	if indexReads(server) != 1 || chartDownloads(server) != 1 {
		t.Fatalf("restart went back to the repository: %v", server.requests)
	}
}

// The layout is Helm's, because the point of following it is that an operator
// can look in the directory and recognise what is there.
func TestCacheLayoutIsReadable(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}

	repositoryDirectory := filepath.Join(directory, testRepositoryID)
	for _, name := range []string{"index.yaml", "index.json"} {
		if _, err := os.Stat(filepath.Join(repositoryDirectory, name)); err != nil {
			t.Fatalf("%s is missing: %v", name, err)
		}
	}
	archives := cachedFiles(t, repositoryDirectory, ".tgz")
	if len(archives) != 1 {
		t.Fatalf("cached archives = %v, want one", archives)
	}
	if base := filepath.Base(archives[0]); !strings.HasPrefix(base, "demo-1.2.0-") {
		t.Fatalf("archive is named %q, want it to name the chart and version", base)
	}
	// Next to data/pki, holding what a platform runs.
	info, err := os.Stat(repositoryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("cache directory mode = %v, want 0700", info.Mode().Perm())
	}
}

// A chart name comes out of a repository's index, so it is never this Server's
// to trust as a path component.
func TestCacheChartPathCannotEscapeItsRepository(t *testing.T) {
	t.Parallel()

	cache, err := NewCache(t.TempDir(), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	path := cache.chartPath(testRepositoryID, "../../etc/passwd", "1.0.0/../..")
	expected := filepath.Join(cache.Directory(), testRepositoryID, "charts")
	if filepath.Dir(path) != expected {
		t.Fatalf("chart path %q escaped %q", path, expected)
	}
	if strings.Contains(filepath.Base(path), "/") {
		t.Fatalf("chart file name %q is not a single component", filepath.Base(path))
	}
	// Two names that sanitise to the same thing are still two charts.
	other := cache.chartPath(testRepositoryID, "..-..-etc-passwd", "1.0.0/../..")
	if other == path {
		t.Fatal("two different chart names collided onto one file")
	}
}

// A cached archive is verified before it is served. It is about to be handed to
// an Agent and applied to a Cluster; a half-written or edited file must not be.
func TestCacheRejectsATamperedArchive(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}
	archives := cachedFiles(t, directory, ".tgz")
	if len(archives) != 1 {
		t.Fatalf("cached archives = %v", archives)
	}
	if err := os.WriteFile(archives[0], []byte("not a chart at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	cache := service.cache
	if _, found := cache.Chart(testRepositoryID, "demo", "1.2.0", ""); found {
		t.Fatal("cache served an archive that no longer matches its digest")
	}
	// And it is dropped rather than left for the next read to reject again.
	if _, err := os.Stat(archives[0]); !os.IsNotExist(err) {
		t.Fatalf("tampered archive was left on disk: %v", err)
	}
}

// The index publishes a digest for each version. A cached file that no longer
// matches it is not that version any more, whatever the file name says.
func TestCacheRefetchesWhenTheIndexDigestMoved(t *testing.T) {
	t.Parallel()

	cache, err := NewCache(t.TempDir(), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("archive")
	cache.PutChart(testRepositoryID, "demo", "1.2.0", CachedChart{Archive: body})

	if _, found := cache.Chart(testRepositoryID, "demo", "1.2.0", "sha256:"+sha256Hex(body)); !found {
		t.Fatal("cache refused an archive matching the published digest")
	}
	// Helm's own index writes the digest bare, without the algorithm prefix.
	if _, found := cache.Chart(testRepositoryID, "demo", "1.2.0", sha256Hex(body)); !found {
		t.Fatal("cache refused a bare hex digest, which is what Helm indexes write")
	}
	if _, found := cache.Chart(testRepositoryID, "demo", "1.2.0", strings.Repeat("a", 64)); found {
		t.Fatal("cache served an archive the index no longer publishes")
	}
	// A repository that publishes no digest still gets the guarantee the cache
	// can make on its own, so caching is not lost to a missing field.
	cache.PutChart(testRepositoryID, "demo", "1.2.0", CachedChart{Archive: body})
	if _, found := cache.Chart(testRepositoryID, "demo", "1.2.0", ""); !found {
		t.Fatal("cache refused an archive because the index carried no digest")
	}
}

// Deleting a repository takes its files with it. Nothing else explains them
// once the entry is gone.
func TestDeletingARepositoryRemovesItsCachedFiles(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}
	repositoryDirectory := filepath.Join(directory, testRepositoryID)
	if _, err := os.Stat(repositoryDirectory); err != nil {
		t.Fatalf("nothing was cached: %v", err)
	}

	if err := service.DeleteRepository(context.Background(), testRepositoryID); err != nil {
		t.Fatalf("DeleteRepository() = %v", err)
	}
	if _, err := os.Stat(repositoryDirectory); !os.IsNotExist(err) {
		t.Fatalf("cache directory survived the delete: %v", err)
	}
}

// An entry removed while this Server was down leaves a directory nobody will
// look at again. Startup is where that is noticed.
func TestPruneRemovesDirectoriesForRepositoriesThatAreGone(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}

	orphan := filepath.Join(directory, "6f2a1b04-5d6e-4f70-a812-93b4c5d6e7f8")
	if err := os.MkdirAll(orphan, 0o700); err != nil {
		t.Fatal(err)
	}
	// Something that is not a repository id is left alone: the cache root is a
	// directory an operator may keep other things in.
	unrelated := filepath.Join(directory, "notes.txt")
	if err := os.WriteFile(unrelated, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := service.PruneCache(context.Background()); err != nil {
		t.Fatalf("PruneCache() = %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatalf("orphan directory survived the prune: %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, testRepositoryID)); err != nil {
		t.Fatalf("prune removed a live repository's cache: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("prune removed something that is not a repository directory: %v", err)
	}
}

// Past the bound the least recently used archives go, and the index stays: it
// is small, and it is what keeps the catalogue readable when the repository is
// not reachable.
func TestCacheEvictsTheLeastRecentlyUsedArchives(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	cache, err := NewCache(directory, 300, nil)
	if err != nil {
		t.Fatal(err)
	}
	cache.PutIndex(testRepositoryID, []byte("apiVersion: v1\n"), IndexMeta{URL: "https://x"})
	for _, version := range []string{"1.0.0", "2.0.0", "3.0.0"} {
		cache.PutChart(testRepositoryID, "demo", version, CachedChart{Archive: make([]byte, 200)})
		// Distinct modification times, which is what eviction orders by.
		time.Sleep(10 * time.Millisecond)
	}
	if archives := cachedFiles(t, directory, ".tgz"); len(archives) != 1 {
		t.Fatalf("cached archives = %v, want the bound to have evicted all but one", archives)
	}
	if _, found := cache.Chart(testRepositoryID, "demo", "3.0.0", ""); !found {
		t.Fatal("eviction dropped the most recently written archive")
	}
	if _, _, found := cache.Index(testRepositoryID); !found {
		t.Fatal("eviction dropped the index")
	}
}

// The cache is optional. With no directory the Service behaves as it did before
// there was one, rather than failing or holding a half-built cache.
func TestCacheCanBeTurnedOff(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), "")
	if service.cache != nil {
		t.Fatal("an empty directory built a cache")
	}
	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if err := service.PruneCache(context.Background()); err != nil {
		t.Fatalf("PruneCache() with no cache = %v", err)
	}
	service.forgetRepository(testRepositoryID)
}

// An expired index costs a conditional request. A repository that has not
// changed answers 304 and sends no body — which for a public index is the
// difference between a few hundred bytes and tens of megabytes.
func TestExpiredIndexIsRevalidatedRatherThanRedownloaded(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	server.indexETag = `"v1"`
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), t.TempDir())
	service.indexTTL = time.Nanosecond

	for range 3 {
		if _, err := service.ListCharts(context.Background(), testRepositoryID, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	if indexReads(server) != 3 {
		t.Fatalf("index was requested %d times, want 3", indexReads(server))
	}
	if server.indexBodies != 1 {
		t.Fatalf("index body was sent %d times, want 1", server.indexBodies)
	}
	if len(server.conditional) != 2 || server.conditional[0] != `"v1"` {
		t.Fatalf("conditional requests = %v, want the stored ETag", server.conditional)
	}
}

// Confirming an index has not changed must not cost a re-parse of it. A public
// index is tens of megabytes of YAML, and parsing it is the expensive half of
// reading one — a 304 that re-derived it would make revalidation cost more than
// the download it saved.
func TestRevalidationReusesTheParsedIndex(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	server.indexETag = `"v1"`
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), t.TempDir())
	service.indexTTL = time.Nanosecond

	first, _, err := service.catalogue.index(context.Background(), service, testRepositoryID, false)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := service.catalogue.index(context.Background(), service, testRepositoryID, false)
	if err != nil {
		t.Fatal(err)
	}
	if server.indexBodies != 1 {
		t.Fatalf("index body was sent %d times, want 1", server.indexBodies)
	}
	// The same object, which is only possible if nothing re-derived it.
	if first != second {
		t.Fatal("a 304 re-parsed an index the repository had just confirmed")
	}

	// With nothing held in memory there is still an answer: the body on disk is
	// parsed rather than downloaded again.
	restarted, _ := newTestServiceWithCache(t, testRepository(server.URL), service.cache.Directory())
	restarted.indexTTL = time.Nanosecond
	if _, _, err := restarted.catalogue.index(
		context.Background(), restarted, testRepositoryID, false,
	); err != nil {
		t.Fatal(err)
	}
	if server.indexBodies != 1 {
		t.Fatalf("index body was sent %d times after a restart, want 1", server.indexBodies)
	}
}

// A repository that cannot be reached leaves a catalogue rather than an empty
// page — and says the listing is old, because "this chart is missing" and "this
// list is from Tuesday" look identical otherwise.
func TestUnreachableRepositoryServesTheCachedIndexAsStale(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if _, err := service.ListCharts(context.Background(), testRepositoryID, "", 0); err != nil {
		t.Fatal(err)
	}

	// The repository goes away, and the Server restarts so nothing is left in
	// memory to answer from.
	server.Close()
	restarted, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	restarted.indexTTL = time.Nanosecond

	page, err := restarted.ListCharts(context.Background(), testRepositoryID, "", 0)
	if err != nil {
		t.Fatalf("ListCharts() with an unreachable repository = %v", err)
	}
	if len(page.Charts) == 0 {
		t.Fatal("catalogue emptied out instead of degrading")
	}
	if !page.Stale {
		t.Fatal("a listing served from the cached copy did not say so")
	}

	// With nothing cached there is nothing to degrade to, and the failure is
	// reported rather than turned into an empty catalogue.
	empty, _ := newTestServiceWithCache(t, testRepository(server.URL), t.TempDir())
	if _, err := empty.ListCharts(context.Background(), testRepositoryID, "", 0); err == nil {
		t.Fatal("an unreachable repository with no cache reported success")
	}
}

// Re-reading the index is the operator saying they do not trust what is held.
// Answering out of any cache, or letting the repository answer 304, would leave
// exactly the document they were asking to replace.
func TestRefreshReadsTheIndexUnconditionally(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	server.indexETag = `"v1"`
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), t.TempDir())

	if _, err := service.ListCharts(context.Background(), testRepositoryID, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshCharts(context.Background(), testRepositoryID, "", 0); err != nil {
		t.Fatalf("RefreshCharts() = %v", err)
	}
	if indexReads(server) != 2 {
		t.Fatalf("index was requested %d times, want 2", indexReads(server))
	}
	if len(server.conditional) != 0 {
		t.Fatalf("refresh sent validators: %v", server.conditional)
	}
	// The archives are keyed by version and a version does not change, so
	// re-reading the index must not throw them away.
	service.cache.PutChart(testRepositoryID, "demo", "1.2.0", CachedChart{Archive: []byte("archive")})
	if _, err := service.RefreshCharts(context.Background(), testRepositoryID, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, found := service.cache.Chart(testRepositoryID, "demo", "1.2.0", ""); !found {
		t.Fatal("refreshing the index discarded the cached archives")
	}
}

// A cached body that came from a different address answers a different
// question, even if nothing got around to invalidating it.
func TestCachedIndexIsIgnoredWhenTheRepositoryMoved(t *testing.T) {
	t.Parallel()

	cache, err := NewCache(t.TempDir(), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	cache.PutIndex(testRepositoryID, []byte("apiVersion: v1\n"), IndexMeta{
		URL:       "https://old.example.test/index.yaml",
		FetchedAt: time.Now(),
	})
	_, meta, found := cache.Index(testRepositoryID)
	if !found {
		t.Fatal("index was not stored")
	}
	if meta.URL == indexURL("https://new.example.test") {
		t.Fatal("a moved repository matched its old cached index")
	}
}

func TestCacheIgnoresIdentifiersThatAreNotRepositories(t *testing.T) {
	t.Parallel()

	cache, err := NewCache(t.TempDir(), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	// Not a UUID: nothing is written, nothing is read, and above all nothing
	// named by a caller becomes a directory.
	cache.PutIndex("../escape", []byte("apiVersion: v1\n"), IndexMeta{})
	if _, _, found := cache.Index("../escape"); found {
		t.Fatal("cache accepted an identifier that is not a repository id")
	}
	entries, err := os.ReadDir(cache.Directory())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("cache root holds %v, want nothing", entries)
	}
}

// A cache failure is never a request failure: the repository is still reachable
// and the answer is still correct, it just costs what it used to cost.
func TestAnUnwritableCacheDoesNotFailRequests(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if err := os.Chmod(directory, 0o500); err != nil {
		t.Skipf("cannot make the cache directory read-only: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })

	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatalf("GetChart() failed because the cache could not be written: %v", err)
	}
}

func TestNormalizeDigest(t *testing.T) {
	t.Parallel()

	hex := strings.Repeat("ab", 32)
	for input, want := range map[string]string{
		hex:                              hex,
		"sha256:" + hex:                  hex,
		"SHA256:" + strings.ToUpper(hex): hex,
		"  " + hex + "  ":                hex,
		"":                               "",
		"sha512:" + hex:                  "",
		"not-a-digest":                   "",
		strings.Repeat("z", 64):          "",
	} {
		if got := normalizeDigest(input); got != want {
			t.Errorf("normalizeDigest(%q) = %q, want %q", input, got, want)
		}
	}
}

// Repository writes go through the same invalidation, so a corrected address
// does not keep answering with what the mistake returned.
func TestUpdatingARepositoryClearsItsCache(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestServiceWithCache(t, testRepository(server.URL), directory)
	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}

	enabled := true
	if _, err := service.UpdateRepository(context.Background(), testRepositoryID, RepositoryInput{
		Name:    "demo",
		URL:     server.URL,
		Enabled: &enabled,
	}, "00000000-0000-4000-8000-000000000001"); err != nil {
		t.Fatalf("UpdateRepository() = %v", err)
	}
	if _, err := os.Stat(filepath.Join(directory, testRepositoryID)); !os.IsNotExist(err) {
		t.Fatalf("cache survived a repository update: %v", err)
	}
}

func TestNewCacheReportsAnUnusableDirectory(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewCache(file, 1<<20, nil); err == nil {
		t.Fatal("NewCache() accepted a path that is a file")
	}
	// And the Service refuses to start rather than running without the cache it
	// was configured with.
	if _, err := NewService(&stubRepositoryStore{}, &recordingAgent{}, Options{
		CacheDirectory: file,
	}); err == nil {
		t.Fatal("NewService() started with an unusable cache directory")
	}
}

// The signature is stored beside the archive and comes back with it. They are
// one entry: a document that signs a digest is wrong the moment the bytes next
// to it change, so a write with no provenance removes whatever was there.
func TestCachedChartCarriesItsProvenance(t *testing.T) {
	t.Parallel()

	cache, err := NewCache(t.TempDir(), 1<<20, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("chart archive")
	cache.PutChart(testRepositoryID, "demo", "1.2.0", CachedChart{
		Archive:           body,
		Provenance:        []byte("-----BEGIN PGP SIGNED MESSAGE-----"),
		ProvenanceChecked: true,
	})
	cached, found := cache.Chart(testRepositoryID, "demo", "1.2.0", "")
	if !found || !cached.ProvenanceChecked ||
		string(cached.Provenance) != "-----BEGIN PGP SIGNED MESSAGE-----" {
		t.Fatalf("Chart() = %+v, %v", cached, found)
	}

	// Re-fetched without a signature — the repository stopped publishing one,
	// or nobody asked this time. The old document must not survive to be
	// verified against bytes it never described.
	cache.PutChart(testRepositoryID, "demo", "1.2.0", CachedChart{Archive: body})
	cached, found = cache.Chart(testRepositoryID, "demo", "1.2.0", "")
	if !found || len(cached.Provenance) != 0 || cached.ProvenanceChecked {
		t.Fatalf("Chart() after an unsigned write = %+v, %v", cached, found)
	}
}
