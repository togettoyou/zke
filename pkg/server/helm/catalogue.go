package helm

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/yaml"
)

var (
	// ErrChartNotFound is a chart, or a version of one, the repository's index
	// does not list. It is separate from a repository that cannot be reached:
	// one means the operator asked for something that is not there, the other
	// means nobody can tell yet.
	ErrChartNotFound = errors.New("chart not found in this repository")
	// ErrRepositoryUnreachable is anything that went wrong between this Server
	// and the repository — DNS, TLS, a 500, a body that is not an index. It is
	// reported as one error because the operator's next step is the same for
	// all of them, and the detail travels in the message.
	ErrRepositoryUnreachable = errors.New("Helm chart repository could not be read")
	// ErrChartTooLarge is a chart archive past what may be sent to an Agent.
	ErrChartTooLarge = errors.New("chart archive exceeds the transferable size")
	// ErrChartOCIUnsupported is a chart the index lists only as an OCI
	// reference. It is its own failure rather than an unreachable repository:
	// the repository answered, the index is valid and the chart is really
	// there — ZKE just does not read OCI registries yet, and no amount of
	// retrying or fixing the address changes that.
	//
	// It is not hypothetical. A repository can keep publishing an index full of
	// charts long after moving the archives themselves into a registry, so the
	// catalogue lists everything and none of it can be fetched.
	ErrChartOCIUnsupported = errors.New("chart is published to an OCI registry, which ZKE does not read")
)

const (
	// One repository index. Public indexes are large — a repository with a few
	// hundred charts and their whole version history runs to tens of megabytes
	// — so the bound is generous, and it is a bound rather than a stream
	// because the index is parsed whole.
	maxIndexBytes int64 = 64 << 20
	// How long a fetched index is reused. Long enough that browsing a
	// repository does not refetch it for every keystroke, short enough that a
	// chart published minutes ago appears without an administrator doing
	// anything.
	indexCacheTTL = 5 * time.Minute
	// How many repositories' indexes are held at once. A platform with more
	// repositories than this still works; the least recently used index is
	// refetched.
	maxCachedIndexes = 16
	// How long a parsed chart is reused. The same window as the index, for the
	// same reason: a version's contents are meant to be immutable, so this
	// bounds how long a republished version stays invisible.
	chartCacheTTL = 5 * time.Minute
	// How much of one chart's unpacked contents may be held. A chart is
	// templates and defaults; one larger than this is served to the request
	// that fetched it and then dropped rather than kept.
	maxCachedChartBytes = 16 << 20
	// How much the whole chart cache may hold. It is a byte budget rather than
	// an entry count because charts differ by orders of magnitude in size, and
	// an entry count would bound the wrong thing.
	maxCachedChartsBytes = 32 << 20
	// Bounds on one upstream request. They apply to the whole exchange, so a
	// repository that accepts the connection and then never sends a body
	// cannot hold a Server request open.
	upstreamRequestTimeout = 60 * time.Second
	upstreamDialTimeout    = 10 * time.Second
)

// ChartSummary is one chart as a catalogue listing shows it: the newest version
// and enough about it to choose. Every version is available separately, because
// a listing that carried them all would be most of the index again.
type ChartSummary struct {
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	AppVersion   string   `json:"app_version"`
	Description  string   `json:"description"`
	IconURL      string   `json:"icon_url"`
	Keywords     []string `json:"keywords"`
	Deprecated   bool     `json:"deprecated"`
	VersionCount int      `json:"version_count"`
}

type ChartPage struct {
	RepositoryID string         `json:"repository_id"`
	Charts       []ChartSummary `json:"charts"`
	// Total is how many charts the repository publishes, before the search
	// term and the page bound were applied. A listing that says "20 of 4000"
	// is the difference between "this repository is small" and "your search was
	// too broad".
	Total int `json:"total"`
	// FetchedAt is when this Server last read the index. It is reported because
	// the index is cached, and an operator who just published a chart needs to
	// know whether they are looking at a moment before that.
	FetchedAt time.Time `json:"fetched_at"`
}

// ChartVersionSummary is one published version of one chart.
type ChartVersionSummary struct {
	Version     string    `json:"version"`
	AppVersion  string    `json:"app_version"`
	Description string    `json:"description"`
	Created     time.Time `json:"created,omitempty"`
	Deprecated  bool      `json:"deprecated"`
	Digest      string    `json:"digest"`
}

type ChartVersionPage struct {
	RepositoryID string                `json:"repository_id"`
	Chart        string                `json:"chart"`
	Versions     []ChartVersionSummary `json:"versions"`
}

// ChartDetail is what an operator reads before installing: the chart's own
// defaults, its README, and the metadata that says what it is.
type ChartDetail struct {
	RepositoryID string   `json:"repository_id"`
	Name         string   `json:"name"`
	Version      string   `json:"version"`
	AppVersion   string   `json:"app_version"`
	Description  string   `json:"description"`
	IconURL      string   `json:"icon_url"`
	Home         string   `json:"home"`
	Sources      []string `json:"sources"`
	Keywords     []string `json:"keywords"`
	Deprecated   bool     `json:"deprecated"`
	Type         string   `json:"type"`
	// Values is the chart's own values.yaml, verbatim. It is the starting point
	// an operator edits, and it is returned as text rather than as a parsed
	// object so its comments — which are the documentation for half of what is
	// in it — survive the trip.
	Values string `json:"values"`
	// README is the chart's README.md if it packages one.
	README string `json:"readme"`
	// Dependencies names the subcharts this chart pulls in, so an operator can
	// see that installing one thing installs four.
	Dependencies []ChartDependency `json:"dependencies"`
	// Files is every member of the chart archive: templates, subcharts and
	// whatever else it packages. The listing travels with the detail because
	// this request already downloaded and parsed the archive — asking for it
	// separately would download the chart twice to show a tree. Contents are
	// fetched one file at a time, because most of them are never opened.
	Files []ChartFileEntry `json:"files"`
	// FileCount is how many files the archive holds before the listing bound
	// was applied, and FilesTruncated says the bound applied.
	FileCount      int  `json:"file_count"`
	FilesTruncated bool `json:"files_truncated"`
}

type ChartDependency struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Repository string `json:"repository"`
	Condition  string `json:"condition"`
}

const (
	// Bounds on the two documents read out of a chart archive and returned as
	// text. Both are meant to be read by a person.
	maxChartValuesBytes = 512 << 10
	maxChartREADMEBytes = 512 << 10
	// The largest listing one request returns.
	maxChartPageSize = 200
)

// RefreshCharts discards the cached index and reads it again before listing.
//
// It exists because the cache is otherwise invisible: a chart published a minute
// ago is not in the catalogue, and a listing that had no way to say "read it
// again" would leave an operator waiting out a TTL they cannot see. Forcing the
// read changes nothing anywhere — it discards derived data this Server holds and
// fetches the same document the TTL would have fetched on its own.
func (service *Service) RefreshCharts(
	ctx context.Context,
	repositoryID string,
	search string,
	limit int,
) (ChartPage, error) {
	if !isUUID(repositoryID) {
		return ChartPage{}, ErrInvalidInput
	}
	service.forgetRepository(repositoryID)
	return service.ListCharts(ctx, repositoryID, search, limit)
}

// ListCharts reports what one repository publishes, newest version first for
// each chart, optionally narrowed by a search term.
func (service *Service) ListCharts(
	ctx context.Context,
	repositoryID string,
	search string,
	limit int,
) (ChartPage, error) {
	index, fetchedAt, err := service.catalogue.index(ctx, service, repositoryID)
	if err != nil {
		return ChartPage{}, err
	}
	if limit <= 0 || limit > maxChartPageSize {
		limit = maxChartPageSize
	}
	term := strings.ToLower(strings.TrimSpace(search))
	names := make([]string, 0, len(index.Entries))
	for name, versions := range index.Entries {
		if len(versions) == 0 || versions[0] == nil || versions[0].Metadata == nil {
			continue
		}
		if term != "" && !chartMatches(versions[0], term) {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	page := ChartPage{
		RepositoryID: repositoryID,
		Charts:       make([]ChartSummary, 0, min(len(names), limit)),
		Total:        len(names),
		FetchedAt:    fetchedAt,
	}
	for _, name := range names {
		if len(page.Charts) == limit {
			break
		}
		versions := index.Entries[name]
		newest := versions[0]
		page.Charts = append(page.Charts, ChartSummary{
			Name:         name,
			Version:      newest.Version,
			AppVersion:   newest.AppVersion,
			Description:  newest.Description,
			IconURL:      newest.Icon,
			Keywords:     newest.Keywords,
			Deprecated:   newest.Deprecated,
			VersionCount: len(versions),
		})
	}
	return page, nil
}

// ListChartVersions reports every version of one chart the index lists, newest
// first — which is the order Helm's own index sorting produces.
func (service *Service) ListChartVersions(
	ctx context.Context,
	repositoryID string,
	chartName string,
) (ChartVersionPage, error) {
	index, _, err := service.catalogue.index(ctx, service, repositoryID)
	if err != nil {
		return ChartVersionPage{}, err
	}
	versions, found := index.Entries[chartName]
	if !found || len(versions) == 0 {
		return ChartVersionPage{}, ErrChartNotFound
	}
	page := ChartVersionPage{
		RepositoryID: repositoryID,
		Chart:        chartName,
		Versions:     make([]ChartVersionSummary, 0, len(versions)),
	}
	for _, version := range versions {
		if version == nil || version.Metadata == nil {
			continue
		}
		page.Versions = append(page.Versions, ChartVersionSummary{
			Version:     version.Version,
			AppVersion:  version.AppVersion,
			Description: version.Description,
			Created:     version.Created,
			Deprecated:  version.Deprecated,
			Digest:      version.Digest,
		})
	}
	return page, nil
}

// GetChart downloads one chart version and reports what an operator needs to
// read before installing it. An empty version means the newest one published.
func (service *Service) GetChart(
	ctx context.Context,
	repositoryID string,
	chartName string,
	version string,
) (ChartDetail, error) {
	loaded, resolved, err := service.loadChart(ctx, repositoryID, chartName, version)
	if err != nil {
		return ChartDetail{}, err
	}
	files, fileCount := chartFileEntries(loaded)
	detail := ChartDetail{
		RepositoryID:   repositoryID,
		Name:           chartName,
		Version:        resolved,
		Values:         truncateText(string(chartFile(loaded, "values.yaml")), maxChartValuesBytes),
		README:         truncateText(string(chartFile(loaded, "README.md")), maxChartREADMEBytes),
		Files:          files,
		FileCount:      fileCount,
		FilesTruncated: fileCount > len(files),
	}
	if loaded.Metadata != nil {
		detail.Name = loaded.Metadata.Name
		detail.Version = loaded.Metadata.Version
		detail.AppVersion = loaded.Metadata.AppVersion
		detail.Description = loaded.Metadata.Description
		detail.IconURL = loaded.Metadata.Icon
		detail.Home = loaded.Metadata.Home
		detail.Sources = loaded.Metadata.Sources
		detail.Keywords = loaded.Metadata.Keywords
		detail.Deprecated = loaded.Metadata.Deprecated
		detail.Type = loaded.Metadata.Type
		for _, dependency := range loaded.Metadata.Dependencies {
			if dependency == nil {
				continue
			}
			detail.Dependencies = append(detail.Dependencies, ChartDependency{
				Name:       dependency.Name,
				Version:    dependency.Version,
				Repository: dependency.Repository,
				Condition:  dependency.Condition,
			})
		}
	}
	return detail, nil
}

// loadChart resolves a version and parses the archive, for reading.
//
// The parse is cached for a few minutes because browsing a chart's files is a
// sequence of requests about the same archive, and downloading it again for
// every file opened would put that cost on the repository. A release operation
// does not come through here: it calls fetchChartArchive directly, so what an
// Agent applies is the bytes this Server just fetched rather than a copy it
// happened to still be holding.
func (service *Service) loadChart(
	ctx context.Context,
	repositoryID string,
	chartName string,
	version string,
) (*chart.Chart, string, error) {
	// Resolved before the cache is consulted, so that "newest" and the number
	// it resolves to are the same cache entry. Without this the chart detail
	// (which asks for "newest") and the file reads that follow it (which ask
	// for the version the detail reported) would be two entries and two
	// downloads of the same archive.
	resolved, err := service.resolveChartVersion(ctx, repositoryID, chartName, version)
	if err != nil {
		return nil, "", err
	}
	if cached, found := service.charts.get(repositoryID, chartName, resolved); found {
		return cached, resolved, nil
	}
	archive, _, err := service.fetchChartArchive(ctx, repositoryID, chartName, resolved)
	if err != nil {
		return nil, "", err
	}
	loaded, err := loader.LoadArchive(bytes.NewReader(archive))
	if err != nil {
		return nil, "", unreachable(
			"chart archive could not be read: %s",
			err,
		)
	}
	service.charts.put(repositoryID, chartName, resolved, loaded)
	return loaded, resolved, nil
}

// resolveChartVersion turns a requested version — possibly empty, meaning the
// newest published — into the one the index names. It reads the index, which is
// cached, so it is not a request upstream on its own.
func (service *Service) resolveChartVersion(
	ctx context.Context,
	repositoryID string,
	chartName string,
	version string,
) (string, error) {
	index, _, err := service.catalogue.index(ctx, service, repositoryID)
	if err != nil {
		return "", err
	}
	entry, err := selectChartVersion(index, chartName, version)
	if err != nil {
		return "", err
	}
	return entry.Version, nil
}

// fetchChartArchive returns the chart archive bytes and the version it resolved
// to. The bytes are what is sent to the Agent, unmodified: this Server never
// repacks a chart, so what runs in the Cluster is what the repository published.
func (service *Service) fetchChartArchive(
	ctx context.Context,
	repositoryID string,
	chartName string,
	version string,
) ([]byte, string, error) {
	repository, err := service.enabledRepository(ctx, repositoryID)
	if err != nil {
		return nil, "", err
	}
	index, _, err := service.catalogue.index(ctx, service, repositoryID)
	if err != nil {
		return nil, "", err
	}
	entry, err := selectChartVersion(index, chartName, version)
	if err != nil {
		return nil, "", err
	}
	reference := chartDownloadURL(entry)
	if reference == "" {
		return nil, "", unreachable(
			"index lists %s %s without a download URL",
			chartName,
			entry.Version,
		)
	}
	// A repository may keep publishing an index that names every chart long
	// after the archives themselves moved into an OCI registry — that is what
	// Helm 3.8 onwards made ordinary. The index still answers what is published
	// and at which versions; only the download changes, so the branch is here
	// and nothing above it has to know.
	if isOCIReference(reference) {
		body, err := service.fetchOCIChartArchive(
			ctx,
			repository,
			reference,
			entry.Version,
			int64(helmrelease.MaxChartBytes),
		)
		if err != nil {
			return nil, "", err
		}
		return body, entry.Version, nil
	}
	target, err := repo.ResolveReferenceURL(repository.URL, reference)
	if err != nil {
		return nil, "", unreachable("chart URL in the index is not usable")
	}
	body, err := service.get(ctx, repository, target, int64(helmrelease.MaxChartBytes))
	if errors.Is(err, errUpstreamTooLarge) {
		return nil, "", ErrChartTooLarge
	}
	if err != nil {
		return nil, "", err
	}
	return body, entry.Version, nil
}

// chartDownloadURL picks the address one chart version is fetched from.
//
// An index entry may list several — mirrors, or the same chart in more than one
// form. An `oci://` reference is a usable address here, so the first non-empty
// candidate wins; an empty return means the index gave no URL at all, which is
// the caller's complaint to make.
func chartDownloadURL(entry *repo.ChartVersion) string {
	for _, candidate := range entry.URLs {
		if trimmed := strings.TrimSpace(candidate); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func selectChartVersion(
	index *repo.IndexFile,
	chartName string,
	version string,
) (*repo.ChartVersion, error) {
	versions, found := index.Entries[chartName]
	if !found || len(versions) == 0 {
		return nil, ErrChartNotFound
	}
	if strings.TrimSpace(version) == "" {
		if versions[0] == nil || versions[0].Metadata == nil {
			return nil, ErrChartNotFound
		}
		return versions[0], nil
	}
	for _, candidate := range versions {
		if candidate != nil && candidate.Metadata != nil &&
			candidate.Version == version {
			return candidate, nil
		}
	}
	return nil, ErrChartNotFound
}

func (service *Service) enabledRepository(
	ctx context.Context,
	repositoryID string,
) (store.HelmRepository, error) {
	if !isUUID(repositoryID) {
		return store.HelmRepository{}, ErrInvalidInput
	}
	repository, err := service.repositories.GetHelmRepositoryCredentials(ctx, repositoryID)
	if err != nil {
		return store.HelmRepository{}, err
	}
	if !repository.Enabled {
		return store.HelmRepository{}, ErrRepositoryDisabled
	}
	return repository, nil
}

func chartMatches(version *repo.ChartVersion, term string) bool {
	if strings.Contains(strings.ToLower(version.Name), term) ||
		strings.Contains(strings.ToLower(version.Description), term) {
		return true
	}
	for _, keyword := range version.Keywords {
		if strings.Contains(strings.ToLower(keyword), term) {
			return true
		}
	}
	return false
}

func chartFile(loaded *chart.Chart, name string) []byte {
	if loaded == nil {
		return nil
	}
	for _, file := range loaded.Raw {
		if file != nil && strings.EqualFold(file.Name, name) {
			return file.Data
		}
	}
	return nil
}

func truncateText(value string, maximum int) string {
	if len(value) <= maximum {
		return value
	}
	return value[:maximum]
}

// indexCache holds parsed repository indexes.
//
// It is in memory and per Server process on purpose. An index is derived data
// with an authoritative upstream, so a replica with a colder cache is slower
// and never wrong, and nothing here has to be invalidated across replicas when
// a repository changes — the TTL does that.
type indexCache struct {
	mutex   sync.Mutex
	entries map[string]*cachedIndex
	// inflight collapses concurrent fetches of the same repository. Without it
	// a Console opening the chart browser fires one request per panel and each
	// of them downloads the whole index.
	inflight map[string]*sync.WaitGroup
}

type cachedIndex struct {
	index     *repo.IndexFile
	fetchedAt time.Time
	usedAt    time.Time
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

func (cache *indexCache) index(
	ctx context.Context,
	service *Service,
	repositoryID string,
) (*repo.IndexFile, time.Time, error) {
	if !isUUID(repositoryID) {
		return nil, time.Time{}, ErrInvalidInput
	}
	for {
		cache.mutex.Lock()
		if entry, found := cache.entries[repositoryID]; found &&
			time.Since(entry.fetchedAt) < indexCacheTTL {
			entry.usedAt = time.Now()
			index := entry.index
			fetchedAt := entry.fetchedAt
			cache.mutex.Unlock()
			return index, fetchedAt, nil
		}
		if waiter, found := cache.inflight[repositoryID]; found {
			cache.mutex.Unlock()
			// Someone else is fetching. Wait for them rather than making the
			// same request, but never past this request's own deadline.
			done := make(chan struct{})
			go func() {
				waiter.Wait()
				close(done)
			}()
			select {
			case <-done:
				continue
			case <-ctx.Done():
				return nil, time.Time{}, ctx.Err()
			}
		}
		waiter := &sync.WaitGroup{}
		waiter.Add(1)
		cache.inflight[repositoryID] = waiter
		cache.mutex.Unlock()

		index, fetchedAt, err := service.fetchIndex(ctx, repositoryID)

		cache.mutex.Lock()
		delete(cache.inflight, repositoryID)
		if err == nil {
			cache.entries[repositoryID] = &cachedIndex{
				index:     index,
				fetchedAt: fetchedAt,
				usedAt:    time.Now(),
			}
			cache.evictLocked()
		}
		cache.mutex.Unlock()
		waiter.Done()
		return index, fetchedAt, err
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

// fetchIndex downloads and parses one repository's index.yaml.
func (service *Service) fetchIndex(
	ctx context.Context,
	repositoryID string,
) (*repo.IndexFile, time.Time, error) {
	repository, err := service.enabledRepository(ctx, repositoryID)
	if err != nil {
		return nil, time.Time{}, err
	}
	target := strings.TrimRight(repository.URL, "/") + "/index.yaml"
	body, err := service.get(ctx, repository, target, maxIndexBytes)
	if errors.Is(err, errUpstreamTooLarge) {
		return nil, time.Time{}, unreachable(
			"index exceeds %d bytes",
			maxIndexBytes,
		)
	}
	if err != nil {
		return nil, time.Time{}, err
	}
	index := &repo.IndexFile{}
	if err := yaml.Unmarshal(body, index); err != nil {
		return nil, time.Time{}, unreachable("response is not a Helm repository index")
	}
	if index.APIVersion == "" {
		// Helm 2's index format. It is rejected rather than half-read: its
		// entries carry a different shape, and guessing would produce a
		// catalogue whose versions do not resolve.
		return nil, time.Time{}, unreachable("repository publishes a Helm 2 index, which ZKE does not read")
	}
	// Drop entries the index lists without usable metadata before anything
	// downstream has to keep checking for them.
	for name, versions := range index.Entries {
		usable := versions[:0]
		for _, version := range versions {
			if version != nil && version.Metadata != nil && version.Version != "" {
				usable = append(usable, version)
			}
		}
		if len(usable) == 0 {
			delete(index.Entries, name)
			continue
		}
		index.Entries[name] = usable
	}
	index.SortEntries()
	return index, time.Now().UTC(), nil
}

var errUpstreamTooLarge = errors.New("upstream response exceeds the allowed size")

// unreachableError is a repository failure whose account is worth returning to
// the administrator who configured it. The URL is theirs, the status code is
// the repository's answer, and "could not be read" on its own would send them
// looking at ZKE rather than at the repository.
//
// It implements the HTTP layer's detailed-error interface, so the detail
// replaces the mapping's fixed message. Nothing built here ever carries a
// credential: the credential is sent as a header, and redactError removes one
// that a redirect put into a URL.
type unreachableError struct {
	detail string
}

func (err *unreachableError) Error() string  { return err.detail }
func (err *unreachableError) Detail() string { return err.detail }
func (err *unreachableError) Unwrap() error  { return ErrRepositoryUnreachable }

func unreachable(format string, arguments ...any) error {
	return &unreachableError{detail: fmt.Sprintf(format, arguments...)}
}

// get performs one bounded HTTP GET against a repository.
//
// Everything about the request is decided here rather than by a shared client:
// the trust store, whether verification is skipped, and the credential are all
// properties of the one repository being read, and a client shared between
// repositories would carry one repository's settings into another's request.
func (service *Service) get(
	ctx context.Context,
	repository store.HelmRepository,
	target string,
	maxBytes int64,
) ([]byte, error) {
	parsed, err := url.Parse(target)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, unreachable(
			"%s is not an http or https URL",
			target,
		)
	}
	client, err := service.clientFor(repository)
	if err != nil {
		return nil, err
	}
	requestContext, cancel := context.WithTimeout(ctx, upstreamRequestTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestContext, http.MethodGet, target, nil)
	if err != nil {
		return nil, unreachable("%s", err)
	}
	request.Header.Set("User-Agent", service.userAgent)
	request.Header.Set("Accept", "application/octet-stream, text/yaml, */*")
	if repository.Username != "" || repository.Password != "" {
		request.SetBasicAuth(repository.Username, repository.Password)
	}
	response, err := client.Do(request)
	if err != nil {
		// The error carries the URL, which is fine — an administrator entered
		// it — but never the credential, which is why it is set as a header
		// rather than embedded in the URL.
		return nil, unreachable("%s", redactError(err))
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		_ = response.Body.Close()
	}()
	if response.StatusCode == http.StatusNotFound {
		return nil, ErrChartNotFound
	}
	if response.StatusCode != http.StatusOK {
		return nil, unreachable(
			"repository answered %s",
			response.Status,
		)
	}
	// One byte past the ceiling on purpose: it is the difference between a body
	// that just fits and one that does not.
	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, unreachable("%s", redactError(err))
	}
	if int64(len(body)) > maxBytes {
		return nil, errUpstreamTooLarge
	}
	return body, nil
}

func (service *Service) clientFor(repository store.HelmRepository) (*http.Client, error) {
	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   upstreamDialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   upstreamDialTimeout,
		ResponseHeaderTimeout: upstreamRequestTimeout,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		MaxIdleConnsPerHost:   2,
	}
	if repository.CACertificatePEM != "" || repository.InsecureSkipTLSVerify {
		tlsConfig := &tls.Config{
			MinVersion: tls.VersionTLS12,
			//nolint:gosec // Skipping verification is an explicit, stored
			// choice by a global administrator, reported by every read of the
			// catalogue rather than assumed.
			InsecureSkipVerify: repository.InsecureSkipTLSVerify,
		}
		if repository.CACertificatePEM != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(repository.CACertificatePEM)) {
				return nil, unreachable("repository CA certificate is not valid PEM")
			}
			tlsConfig.RootCAs = pool
		}
		transport.TLSClientConfig = tlsConfig
	}
	return &http.Client{
		Transport: transport,
		Timeout:   upstreamRequestTimeout,
		// A repository that redirects is followed, but not into a different
		// scheme: an https repository whose index redirects to http would hand
		// the credential to a plaintext hop.
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				return errors.New("too many redirects")
			}
			if len(via) > 0 && via[0].URL.Scheme == "https" &&
				request.URL.Scheme != "https" {
				return errors.New("refusing redirect from https to http")
			}
			return nil
		},
	}, nil
}

// redactError keeps a credential out of a message. Go's url.Error prints the
// URL it was given, and the credential is never in the URL — but a repository
// that redirects can put one there, so the userinfo is removed rather than
// trusted to be absent.
func redactError(err error) string {
	message := err.Error()
	var urlError *url.Error
	if errors.As(err, &urlError) && urlError.URL != "" {
		if parsed, parseErr := url.Parse(urlError.URL); parseErr == nil &&
			parsed.User != nil {
			redacted := *parsed
			redacted.User = url.User("redacted")
			message = strings.ReplaceAll(message, urlError.URL, redacted.String())
		}
	}
	const maximum = 512
	if len(message) > maximum {
		return message[:maximum] + "…"
	}
	return message
}
