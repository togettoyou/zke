package helm

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/helmrelease"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/repo"
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
	// Stale says the repository could not be reached and this listing came from
	// the copy on disk. The catalogue still works — that is the point of
	// keeping one — but "this chart is missing" and "this list is from Tuesday"
	// are different problems, and without this they look identical.
	Stale bool `json:"stale"`
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
	service.forgetIndex(repositoryID)
	return service.listCharts(ctx, repositoryID, search, limit, true)
}

// ListCharts reports what one repository publishes, newest version first for
// each chart, optionally narrowed by a search term.
func (service *Service) ListCharts(
	ctx context.Context,
	repositoryID string,
	search string,
	limit int,
) (ChartPage, error) {
	return service.listCharts(ctx, repositoryID, search, limit, false)
}

func (service *Service) listCharts(
	ctx context.Context,
	repositoryID string,
	search string,
	limit int,
	force bool,
) (ChartPage, error) {
	index, state, err := service.catalogue.index(ctx, service, repositoryID, force)
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
		FetchedAt:    state.FetchedAt,
		Stale:        state.Stale,
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
	index, _, err := service.catalogue.index(ctx, service, repositoryID, false)
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
// The parse is cached in memory for a few minutes because browsing a chart's
// files is a sequence of requests about one archive, and unpacking it again for
// every file opened is work that has already been done. The archive underneath
// comes from fetchChartArchive, which is where the disk cache is — so a release
// operation, which needs the bytes rather than a parse of them, gets the same
// cached archive without going through here.
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
	index, _, err := service.catalogue.index(ctx, service, repositoryID, false)
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
//
// A published version does not change, so the archive is kept on disk under
// data/helm and only fetched once. That is what the index's own digest is used
// for here: it is the repository's statement about this version, so a cached
// file that no longer matches it is not this version any more and is fetched
// again. A repository that publishes no digest gets the weaker guarantee the
// cache can still make on its own — that the file is what was written there.
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
	index, _, err := service.catalogue.index(ctx, service, repositoryID, false)
	if err != nil {
		return nil, "", err
	}
	entry, err := selectChartVersion(index, chartName, version)
	if err != nil {
		return nil, "", err
	}
	if cached, found := service.cache.Chart(
		repositoryID,
		chartName,
		entry.Version,
		entry.Digest,
	); found {
		return cached, entry.Version, nil
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
		service.cache.PutChart(repositoryID, chartName, entry.Version, body)
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
	service.cache.PutChart(repositoryID, chartName, entry.Version, body)
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
