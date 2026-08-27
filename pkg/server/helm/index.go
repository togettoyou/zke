package helm

import (
	"context"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/repo"
	"sigs.k8s.io/yaml"
)

// Reading a repository's index.
//
// The index is the one document everything else in the catalogue derives from:
// what charts exist, which versions they have, and where each version's archive
// can be fetched. It is also the expensive one — a public repository's index
// runs to tens of megabytes — so it is what most needs a cache, and where an
// unconditional re-download hurts most.
//
// Three layers answer a read, and each one exists because the layer behind it
// costs something the layer in front does not:
//
//  1. the parsed index in memory, so a search box does not re-parse megabytes
//     of YAML on every keystroke;
//  2. the body on disk under data/helm, so a restarted Server, or one whose
//     repository is unreachable, still has a catalogue;
//  3. the repository, asked conditionally, so an expired index usually costs a
//     304 rather than another download of a document that has not changed.

// One repository index. Public indexes are large — a repository with a few
// hundred charts and their whole version history runs to tens of megabytes — so
// the bound is generous, and it is a bound rather than a stream because the
// index is parsed whole.
const maxIndexBytes int64 = 64 << 20

// indexState is what a caller needs to know about the index it was handed
// besides its contents.
type indexState struct {
	// FetchedAt is when the repository last confirmed this body, whether by
	// sending it or by answering that it had not changed.
	FetchedAt time.Time
	// Stale says the repository could not be reached and this is the copy from
	// the last time it could. The catalogue still works; it is reported rather
	// than hidden because "this chart is missing" and "this list is from
	// Tuesday" are different problems and look identical without it.
	Stale bool
}

// heldIndex is what the memory layer already has parsed, offered to the fetch
// so that confirming an index has not changed does not cost a re-parse of it.
//
// It is only usable when it came from the same read as the body on disk, which
// is what fetchedAt is compared for: two documents that happen to be in hand at
// the same moment are not necessarily the same document.
type heldIndex struct {
	index     *repo.IndexFile
	fetchedAt time.Time
}

func (held *heldIndex) matching(meta IndexMeta) *repo.IndexFile {
	if held == nil || held.index == nil || !held.fetchedAt.Equal(meta.FetchedAt) {
		return nil
	}
	return held.index
}

// loadIndex produces the parsed index for one repository, going no further
// upstream than it has to.
//
// force is the operator asking for the index to be read again. It skips both
// the freshness check and the validators: a conditional request would let the
// repository answer 304 and leave exactly the document they were saying they
// did not trust.
func (service *Service) loadIndex(
	ctx context.Context,
	repositoryID string,
	force bool,
	held *heldIndex,
) (*repo.IndexFile, indexState, error) {
	repository, err := service.enabledRepository(ctx, repositoryID)
	if err != nil {
		return nil, indexState{}, err
	}
	target := indexURL(repository.URL)

	cachedBody, meta, cached := service.cache.Index(repositoryID)
	// A body fetched from a different address answers a different question,
	// even if nothing got around to invalidating it.
	cached = cached && meta.URL == target
	if cached && !force && time.Since(meta.FetchedAt) < service.indexTTL {
		if index := held.matching(meta); index != nil {
			return index, indexState{FetchedAt: meta.FetchedAt}, nil
		}
		if index, parseErr := parseIndex(cachedBody); parseErr == nil {
			return index, indexState{FetchedAt: meta.FetchedAt}, nil
		}
		// A cached body that no longer parses is not a reason to serve nothing;
		// it is a reason to go and get it again.
		cached = false
	}

	request := upstreamRequest{Target: target, MaxBytes: maxIndexBytes}
	if cached && !force {
		request.ETag = meta.ETag
		request.LastModified = meta.LastModified
	}
	response, err := service.fetch(ctx, repository, request)
	if err != nil {
		if !cached {
			return nil, indexState{}, err
		}
		index := held.matching(meta)
		if index == nil {
			var parseErr error
			if index, parseErr = parseIndex(cachedBody); parseErr != nil {
				return nil, indexState{}, err
			}
		}
		// The repository is unreachable and there is a copy of what it last
		// published. Serving it is the difference between a catalogue that
		// degrades and one that empties out; saying it is old is what keeps
		// that from being a lie.
		service.logger.Warn(
			"serving a cached Helm repository index because the repository could not be read",
			"repository_id", repositoryID,
			"fetched_at", meta.FetchedAt,
			"error", err.Error(),
		)
		return index, indexState{FetchedAt: meta.FetchedAt, Stale: true}, nil
	}

	now := time.Now().UTC()
	if response.NotModified && cached {
		// Nothing changed, so neither did the parse. Re-reading a document the
		// repository has just confirmed would make confirming it cost more than
		// the download it saved: a public index is tens of megabytes of YAML,
		// and parsing it is the expensive half of reading one.
		index := held.matching(meta)
		var parseErr error
		if index == nil {
			index, parseErr = parseIndex(cachedBody)
		}
		if parseErr == nil {
			// Recording the confirmation is what makes asking worth it: without
			// it every request past the TTL revalidates again, and a repository
			// that never changes is asked on every boundary forever.
			meta.FetchedAt = now
			service.cache.TouchIndex(repositoryID, meta)
			return index, indexState{FetchedAt: now}, nil
		}
		// The repository says the body has not changed and the body will not
		// parse, so what is on disk is not what the repository has. Ask again
		// without the validators.
		response, err = service.fetch(ctx, repository, upstreamRequest{
			Target:   target,
			MaxBytes: maxIndexBytes,
		})
		if err != nil {
			return nil, indexState{}, err
		}
	}

	index, err := parseIndex(response.Body)
	if err != nil {
		return nil, indexState{}, err
	}
	service.cache.PutIndex(repositoryID, response.Body, IndexMeta{
		URL:          target,
		FetchedAt:    now,
		ETag:         response.ETag,
		LastModified: response.LastModified,
	})
	return index, indexState{FetchedAt: now}, nil
}

func indexURL(repositoryURL string) string {
	return strings.TrimRight(repositoryURL, "/") + "/index.yaml"
}

// parseIndex turns an index body into the form the catalogue reads, dropping
// what it cannot use before anything downstream has to keep checking for it.
func parseIndex(body []byte) (*repo.IndexFile, error) {
	index := &repo.IndexFile{}
	if err := yaml.Unmarshal(body, index); err != nil {
		return nil, unreachable("response is not a Helm repository index")
	}
	if index.APIVersion == "" {
		// Helm 2's index format. It is rejected rather than half-read: its
		// entries carry a different shape, and guessing would produce a
		// catalogue whose versions do not resolve.
		return nil, unreachable("repository publishes a Helm 2 index, which ZKE does not read")
	}
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
	return index, nil
}
