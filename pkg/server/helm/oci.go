package helm

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/togettoyou/zke/pkg/server/store"
	helmregistry "helm.sh/helm/v3/pkg/registry"
	"oras.land/oras-go/v2/content"
	"oras.land/oras-go/v2/errdef"
	orasregistry "oras.land/oras-go/v2/registry"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
)

// Charts published as OCI artifacts.
//
// Since Helm 3.8 a chart may be pushed to a registry instead of a static
// archive server, and a plain HTTP repository may point at one: it keeps
// publishing an `index.yaml` naming every chart and every version, while the
// `urls` for those versions are `oci://` references. The index therefore still
// answers what exists and which versions there are — searching, listing and
// version history all work unchanged — and only the download is different.
//
// That is the whole of what is handled here. A repository whose *address* is
// `oci://` is a different thing and is not supported: a registry has no index
// to read, and the distribution spec's catalogue endpoint is optional and
// unavailable on the registries that matter, so there would be nothing to
// browse.
//
// Nothing ambient is read. Helm's own registry client consults
// `~/.docker/config.json` and Helm's credentials file, which would make what
// this Server can pull depend on who happened to log in on the host and would
// send those credentials to whatever host an index named. The registry is
// reached with the credential stored on the repository, or with none.

const (
	ociScheme = "oci://"
	// Bound on a manifest read from a registry. A Helm chart manifest is a few
	// hundred bytes; this leaves room for one with many layers without leaving
	// room for a document sent to exhaust the parser.
	maxOCIManifestBytes = 4 << 20
)

func isOCIReference(reference string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(reference)), ociScheme)
}

// fetchOCIChartArchive pulls one chart archive out of an OCI registry, and the
// provenance layer beside it when the repository's policy needs one.
//
// The bytes returned are the archive as the registry stores it — the same thing
// the HTTP path returns — so everything downstream, from the file browser to
// what an Agent applies, is unchanged by which of the two fetched it.
//
// Provenance is a layer of the same manifest here rather than a second URL, so
// finding out whether one exists costs nothing; only fetching it is a second
// request, and that is skipped when nothing is going to check it.
func (service *Service) fetchOCIChartArchive(
	ctx context.Context,
	repository store.HelmRepository,
	reference string,
	version string,
	maxBytes int64,
	withProvenance bool,
) ([]byte, []byte, error) {
	target, tag, err := ociRepository(reference, version)
	if err != nil {
		return nil, nil, err
	}
	// The same client the HTTP path uses for this repository: its custom CA and
	// its "skip TLS verification" choice are properties of the repository an
	// administrator configured, and a registry named by that repository's index
	// is reached under the same terms. The credential is the exception — see
	// ociCredential.
	client, err := service.clientFor(repository)
	if err != nil {
		return nil, nil, err
	}
	authorizer := &auth.Client{
		Client:     client,
		Cache:      auth.NewCache(),
		Credential: ociCredential(repository, target.Reference.Registry),
	}
	authorizer.SetUserAgent(service.userAgent)
	target.Client = authorizer
	target.MaxMetadataBytes = maxOCIManifestBytes

	// One deadline for the whole exchange, as on the HTTP path. A pull is a
	// token request, a manifest and a blob; a registry that answers each of
	// them slowly enough could otherwise hold a Server request open for as long
	// as it liked.
	requestContext, cancel := context.WithTimeout(ctx, upstreamRequestTimeout)
	defer cancel()

	descriptor, manifestReader, err := target.FetchReference(requestContext, tag)
	if err != nil {
		return nil, nil, ociError(err, reference)
	}
	manifestBytes, err := content.ReadAll(manifestReader, descriptor)
	_ = manifestReader.Close()
	if err != nil {
		return nil, nil, unreachable("registry manifest for %s could not be read: %s", reference, err)
	}
	layer, provenance, err := chartLayers(descriptor, manifestBytes, reference)
	if err != nil {
		return nil, nil, err
	}
	// Checked against the descriptor before a byte is fetched, rather than
	// while reading: the manifest already states the size, so an archive too
	// large to send to an Agent is refused without downloading it first.
	if layer.Size > maxBytes {
		return nil, nil, ErrChartTooLarge
	}
	blobReader, err := target.Fetch(requestContext, layer)
	if err != nil {
		return nil, nil, ociError(err, reference)
	}
	defer func() { _ = blobReader.Close() }()
	// ReadAll verifies the bytes against the descriptor's digest and size. That
	// verification is the point: this archive is about to be handed to an Agent
	// and applied to a Cluster, and the digest is the registry's own statement
	// of what was published.
	archive, err := content.ReadAll(blobReader, layer)
	if err != nil {
		return nil, nil, unreachable("chart layer of %s could not be read: %s", reference, err)
	}
	if !withProvenance || provenance.Size == 0 {
		return archive, nil, nil
	}
	// A provenance file is a signature and a short digest table. One larger
	// than the bound is not one, and refusing it here means a registry cannot
	// turn "verify this chart" into an unbounded read.
	if provenance.Size > maxProvenanceBytes {
		return nil, nil, unreachable(
			"provenance layer of %s exceeds %d bytes",
			reference,
			maxProvenanceBytes,
		)
	}
	provenanceReader, err := target.Fetch(requestContext, provenance)
	if err != nil {
		return nil, nil, ociError(err, reference)
	}
	defer func() { _ = provenanceReader.Close() }()
	document, err := content.ReadAll(provenanceReader, provenance)
	if err != nil {
		return nil, nil, unreachable(
			"provenance layer of %s could not be read: %s",
			reference,
			err,
		)
	}
	return archive, document, nil
}

// ociRepository turns an index's `oci://` URL into a repository to pull from.
//
// The tag is the chart version. Helm writes it into the reference when it
// pushes, but an index is a document someone else wrote, so a reference without
// one falls back to the version the index gave for this entry rather than
// failing — those are the same thing, and one of them being absent is not a
// disagreement.
func ociRepository(reference string, version string) (*remote.Repository, string, error) {
	trimmed := strings.TrimSpace(reference)
	if !isOCIReference(trimmed) {
		return nil, "", unreachable("%s is not an OCI reference", reference)
	}
	parsed, err := orasregistry.ParseReference(trimmed[len(ociScheme):])
	if err != nil {
		return nil, "", unreachable("%s is not a usable OCI reference: %s", reference, err)
	}
	tag := parsed.Reference
	if tag == "" {
		tag = strings.TrimSpace(version)
	}
	if tag == "" {
		return nil, "", unreachable("%s names no chart version to pull", reference)
	}
	parsed.Reference = tag
	return &remote.Repository{Reference: parsed}, tag, nil
}

// ociCredential decides whether the repository's credential travels to this
// registry.
//
// It goes only to the host the administrator entered. The registry host comes
// out of the index, which is a document the repository serves: sending the
// stored credential to whatever host that document happens to name would turn
// "this repository needs a password" into "anyone who can write this index can
// collect it". A private setup where one host serves both the index and the
// registry — which is what a Harbor or an Artifactory is — is unaffected.
func ociCredential(repository store.HelmRepository, registryHost string) auth.CredentialFunc {
	if repository.Username == "" && repository.Password == "" {
		return nil
	}
	parsed, err := url.Parse(repository.URL)
	if err != nil || parsed.Host == "" || !strings.EqualFold(parsed.Host, registryHost) {
		return nil
	}
	return auth.StaticCredential(registryHost, auth.Credential{
		Username: repository.Username,
		Password: repository.Password,
	})
}

// chartLayers finds the packaged chart among a manifest's layers, and the
// provenance file if one is published with it.
//
// Helm gives each a media type of its own, so both are identified by what they
// are rather than by their position: a manifest also carries the config blob,
// and taking "the first layer" would eventually hand one of the others to the
// loader. A returned provenance descriptor with a zero size means the manifest
// carries none, which is a normal state and not a failure.
func chartLayers(
	descriptor ocispec.Descriptor,
	manifestBytes []byte,
	reference string,
) (ocispec.Descriptor, ocispec.Descriptor, error) {
	if descriptor.MediaType == ocispec.MediaTypeImageIndex ||
		descriptor.MediaType == "application/vnd.docker.distribution.manifest.list.v2+json" {
		// A multi-platform index. Charts are not built per platform, so rather
		// than picking one arbitrarily this says what was found: an operator
		// pointed at something that is not a chart.
		return ocispec.Descriptor{}, ocispec.Descriptor{}, unreachable(
			"%s is a multi-platform index rather than a Helm chart",
			reference,
		)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, unreachable(
			"registry manifest for %s is not readable: %s",
			reference,
			err,
		)
	}
	var chart, provenance ocispec.Descriptor
	var found bool
	for _, layer := range manifest.Layers {
		switch layer.MediaType {
		case helmregistry.ChartLayerMediaType, helmregistry.LegacyChartLayerMediaType:
			if !found {
				chart, found = layer, true
			}
		case helmregistry.ProvLayerMediaType:
			if provenance.Size == 0 {
				provenance = layer
			}
		}
	}
	if !found {
		return ocispec.Descriptor{}, ocispec.Descriptor{}, unreachable(
			"%s carries no Helm chart layer; it is an artifact of some other kind",
			reference,
		)
	}
	return chart, provenance, nil
}

// ociError maps a registry's refusal onto the failures the rest of the
// catalogue already distinguishes.
//
// A tag that is not there is the same answer as a version missing from an
// index, and reporting it as a repository failure would send an operator to
// check a registry that is working correctly. Everything else is upstream of
// this Server and travels with its own account, minus anything a redirect may
// have put into a URL.
func ociError(err error, reference string) error {
	if errors.Is(err, errdef.ErrNotFound) {
		return ErrChartNotFound
	}
	var response *errcode.ErrorResponse
	if errors.As(err, &response) {
		switch response.StatusCode {
		case 401, 403:
			return unreachable(
				"registry refused access to %s: %s",
				reference,
				strings.TrimSpace(response.Errors.Error()),
			)
		case 404:
			return ErrChartNotFound
		}
	}
	return unreachable("%s could not be pulled: %s", reference, redactError(err))
}
