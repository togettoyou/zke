package helm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

// AgentAccess is the Agent connection surface a release operation needs. The
// Server sends the chart and the values; the Agent runs Helm.
//
// The progress callback is how the Cluster says what it is doing before it is
// done. It may be nil, and it is called from the goroutine reading the Stream,
// so an implementation that blocks in it holds the operation up.
type AgentAccess interface {
	RequestHelm(
		ctx context.Context,
		clusterID string,
		request *agentv1.HelmRequest,
		values io.Reader,
		chart io.Reader,
		report io.Writer,
		idempotencyKey string,
		progress func(*agentv1.HelmProgress),
	) (*agentv1.HelmResponse, error)
}

// Options configures the catalogue's two caches.
//
// Both are optional: an empty CacheDirectory turns the disk cache off entirely
// and the Service works as it did before there was one, at the cost of going to
// the repository for everything.
type Options struct {
	UserAgent string
	// CacheDirectory is where indexes and chart archives are kept between
	// requests and across restarts — data/helm by default.
	CacheDirectory string
	// MaxCacheBytes bounds the cached chart archives.
	MaxCacheBytes int64
	// IndexTTL is how long a repository index is used before the repository is
	// asked about it again. It is how long a newly published chart may stay
	// invisible, which is why an operator can also force the read.
	IndexTTL time.Duration
	Logger   *slog.Logger
}

// The default index freshness window.
//
// An hour rather than a few minutes because of what expiry actually costs and
// what it buys. It is not how long the copy on disk is kept — that is until the
// repository is edited or deleted — only how long before the repository is
// asked whether anything changed. Repositories publish charts rarely, and the
// operator who just published one has a button that reads the index again, so a
// short window mostly buys nothing; what it costs is a request per repository
// per window for every session, and against a repository that sends no ETag or
// Last-Modified it costs the whole index again.
const defaultIndexTTL = time.Hour

type Service struct {
	repositories RepositoryStore
	agents       AgentAccess
	// catalogue and charts are parsed objects in memory; cache is the bytes on
	// disk behind them. See memory.go and cache.go.
	catalogue *indexCache
	charts    *chartCache
	cache     *Cache
	indexTTL  time.Duration
	userAgent string
	logger    *slog.Logger
}

func NewService(
	repositories RepositoryStore,
	agents AgentAccess,
	options Options,
) (*Service, error) {
	if repositories == nil || agents == nil {
		return nil, errors.New("Helm service dependencies are required")
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	userAgent := options.UserAgent
	if userAgent == "" {
		userAgent = "zke-server"
	}
	indexTTL := options.IndexTTL
	if indexTTL <= 0 {
		indexTTL = defaultIndexTTL
	}
	cache, err := NewCache(options.CacheDirectory, options.MaxCacheBytes, logger)
	if err != nil {
		return nil, err
	}
	return &Service{
		repositories: repositories,
		agents:       agents,
		catalogue:    newIndexCache(),
		charts:       newChartCache(),
		cache:        cache,
		indexTTL:     indexTTL,
		userAgent:    userAgent,
		logger:       logger,
	}, nil
}

// PruneCache removes cached files belonging to repositories that no longer
// exist.
//
// Deleting a repository cleans up after itself, but only while this Server is
// running. An entry removed straight from the database, or while the process
// was down, would otherwise leave a directory nobody will ever look at again,
// counting against the cache budget and outliving whatever explained it. This
// is run once at startup, where noticing costs one directory listing.
func (service *Service) PruneCache(ctx context.Context) error {
	if service.cache == nil {
		return nil
	}
	repositories, err := service.repositories.ListHelmRepositories(ctx)
	if err != nil {
		return err
	}
	known := make([]string, 0, len(repositories))
	for _, repository := range repositories {
		known = append(known, repository.ID)
	}
	service.cache.Prune(known)
	return nil
}

// forgetRepository drops everything this Server derived from one repository:
// its parsed index, every chart parsed out of it, and the files on disk. Called
// when an entry is edited or deleted — a repository that points somewhere else,
// authenticates differently, or no longer exists published none of it.
func (service *Service) forgetRepository(repositoryID string) {
	service.catalogue.forget(repositoryID)
	service.charts.forget(repositoryID)
	service.cache.Forget(repositoryID)
}

// forgetIndex drops only the parsed index, for a re-read of the catalogue.
//
// The chart archives stay. They are keyed by version and a version does not
// change, so re-reading the index is a question about which versions exist and
// not about what any of them contains — and throwing away a repository's whole
// archive cache to answer it would make "refresh" the most expensive button on
// the page.
func (service *Service) forgetIndex(repositoryID string) {
	service.catalogue.forget(repositoryID)
}

func isUUID(value string) bool {
	return validation.IsUUID(value)
}
