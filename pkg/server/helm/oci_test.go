package helm

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/togettoyou/zke/pkg/server/store"
	helmregistry "helm.sh/helm/v3/pkg/registry"
)

func sha256Digest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// ociRegistryServer stands in for a repository that publishes an index over
// HTTPS and its archives in an OCI registry on the same host — which is what a
// Harbor or an Artifactory is, and the shape Helm 3.8 onwards made ordinary.
//
// TLS rather than plain HTTP because that is what the code path under test
// requires: the registry is reached with the repository's own HTTP client, and
// the test repository turns verification off the way an operator would for an
// internal registry with a private CA.
type ociRegistryServer struct {
	*httptest.Server
	requests   []string
	basicAuth  []string
	chartLayer string
	// requireAuth makes the registry answer the first unauthenticated request
	// with a challenge, which is what makes the credential travel at all.
	requireAuth bool
	// layerSize overstates the chart layer in the manifest when set, so the
	// size bound can be tested without building a large archive.
	layerSize int64
	// provenance is the `.prov` document this registry publishes as a second
	// layer of the same manifest, if any. Helm pushes it that way, which is why
	// finding out whether a chart is signed costs nothing here and fetching the
	// signature is a separate request.
	provenance []byte
	provLayer  string
}

func newOCIRegistryServer(t *testing.T, archive []byte) *ociRegistryServer {
	t.Helper()
	server := &ociRegistryServer{chartLayer: sha256Digest(archive)}
	config := []byte(`{"name":"moved","version":"2.0.0"}`)

	server.Server = httptest.NewTLSServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		server.requests = append(server.requests, request.URL.Path)
		if username, password, ok := request.BasicAuth(); ok {
			server.basicAuth = append(
				server.basicAuth,
				request.URL.Path+" "+username+":"+password,
			)
		} else if server.requireAuth && strings.HasPrefix(request.URL.Path, "/v2/") {
			writer.Header().Set("WWW-Authenticate", `Basic realm="registry"`)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}

		layerSize := int64(len(archive))
		if server.layerSize != 0 {
			layerSize = server.layerSize
		}
		layers := []ocispec.Descriptor{{
			MediaType: helmregistry.ChartLayerMediaType,
			Digest:    digest.Digest(sha256Digest(archive)),
			Size:      layerSize,
		}}
		if len(server.provenance) > 0 {
			layers = append(layers, ocispec.Descriptor{
				MediaType: helmregistry.ProvLayerMediaType,
				Digest:    digest.Digest(server.provLayer),
				Size:      int64(len(server.provenance)),
			})
		}
		manifest, err := json.Marshal(ocispec.Manifest{
			Versioned: specs.Versioned{SchemaVersion: 2},
			MediaType: ocispec.MediaTypeImageManifest,
			Config: ocispec.Descriptor{
				MediaType: helmregistry.ConfigMediaType,
				Digest:    digest.Digest(sha256Digest(config)),
				Size:      int64(len(config)),
			},
			Layers: layers,
		})
		if err != nil {
			t.Error(err)
			return
		}
		switch {
		case request.URL.Path == "/index.yaml":
			// The index still names the chart and its versions; only the `urls`
			// point at the registry. That is the whole of what changes.
			fmt.Fprintf(writer, `apiVersion: v1
entries:
  moved:
    - name: moved
      version: 2.0.0
      appVersion: "1.4"
      description: a chart published to a registry
      urls: [oci://%s/charts/moved:2.0.0]
`, request.Host)
		case request.URL.Path == "/v2/":
			writer.WriteHeader(http.StatusOK)
		case request.URL.Path == "/v2/charts/moved/manifests/2.0.0":
			writer.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
			writer.Header().Set("Docker-Content-Digest", sha256Digest(manifest))
			writer.Header().Set("Content-Length", fmt.Sprint(len(manifest)))
			_, _ = writer.Write(manifest)
		case request.URL.Path == "/v2/charts/moved/blobs/"+server.chartLayer:
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("Content-Length", fmt.Sprint(len(archive)))
			_, _ = writer.Write(archive)
		case len(server.provenance) > 0 &&
			request.URL.Path == "/v2/charts/moved/blobs/"+server.provLayer:
			writer.Header().Set("Content-Type", "application/octet-stream")
			writer.Header().Set("Content-Length", fmt.Sprint(len(server.provenance)))
			_, _ = writer.Write(server.provenance)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// publishProvenance makes the registry serve a signature alongside the chart,
// the way `helm push` does when the chart was packaged with one.
func (server *ociRegistryServer) publishProvenance(document []byte) {
	server.provenance = document
	server.provLayer = sha256Digest(document)
}

func (server *ociRegistryServer) fetched(path string) int {
	count := 0
	for _, request := range server.requests {
		if request == path {
			count++
		}
	}
	return count
}

// ociRepositoryEntry points the catalogue at the stub. Verification is skipped
// because the stub's certificate is generated per run — the same switch an
// operator sets for an internal registry, and the reason the registry is reached
// with the repository's own client rather than a default one.
func ociRepositoryEntry(url string) store.HelmRepository {
	repository := testRepository(url)
	repository.InsecureSkipTLSVerify = true
	return repository
}

// The chart the index points into a registry reads and installs like any other:
// the archive is the same bytes, so everything downstream of the fetch — the
// file browser, the values, what an Agent applies — is unchanged.
func TestChartsPublishedToAnOCIRegistryArePulled(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "moved", "2.0.0")
	server := newOCIRegistryServer(t, archive)
	service, agent := newTestService(t, ociRepositoryEntry(server.URL))

	detail, err := service.GetChart(context.Background(), testRepositoryID, "moved", "")
	if err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if detail.Version != "2.0.0" {
		t.Fatalf("GetChart() = %+v, want version 2.0.0", detail)
	}
	if !strings.Contains(detail.Values, "# how many copies to run") {
		t.Fatalf("values = %q", detail.Values)
	}
	page, err := service.ListChartFiles(context.Background(), testRepositoryID, "moved", "")
	if err != nil {
		t.Fatalf("ListChartFiles() = %v", err)
	}
	if len(page.Files) == 0 {
		t.Fatal("ListChartFiles() listed no files")
	}

	// The file browser works over a registry-published chart too, and still
	// costs one pull: the parsed chart is cached the same way.
	file, err := service.GetChartFile(
		context.Background(), testRepositoryID, "moved", "", "templates/deployment.yaml",
	)
	if err != nil {
		t.Fatalf("GetChartFile() = %v", err)
	}
	if !strings.Contains(file.Content, "{{ .Release.Name }}") {
		t.Fatalf("content = %q", file.Content)
	}
	if pulls := server.fetched("/v2/charts/moved/blobs/" + server.chartLayer); pulls != 1 {
		t.Fatalf("blob was pulled %d times, want 1", pulls)
	}

	// What reaches the Agent is the archive the registry stores, byte for byte.
	if _, err := service.Install(context.Background(), InstallInput{
		ClusterID:    testClusterID,
		Namespace:    "shop",
		Name:         "checkout",
		RepositoryID: testRepositoryID,
		Chart:        "moved",
	}); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if string(agent.chart) != string(archive) {
		t.Fatalf("Install() sent %d bytes, want the %d-byte archive", len(agent.chart), len(archive))
	}
}

// A registry that challenges gets the credential stored on the repository, and
// nothing from the host this Server happens to be running on.
func TestOCIPullSendsTheStoredCredential(t *testing.T) {
	t.Parallel()

	server := newOCIRegistryServer(t, chartArchive(t, "moved", "2.0.0"))
	server.requireAuth = true
	repository := ociRepositoryEntry(server.URL)
	repository.Username = "robot"
	repository.Password = "s3cret"
	service, _ := newTestService(t, repository)

	if _, err := service.GetChart(context.Background(), testRepositoryID, "moved", ""); err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	// Specifically on a registry request. The index fetch carries the credential
	// too, and asserting on "some request saw it" would pass without the
	// registry ever having been authenticated to.
	want := "/v2/charts/moved/manifests/2.0.0 robot:s3cret"
	if !slices.Contains(server.basicAuth, want) {
		t.Fatalf("registry saw %v, want %q", server.basicAuth, want)
	}
	// And only after being challenged: an unconditional Authorization header
	// would send the credential to a registry that never asked for one.
	if server.fetched("/v2/charts/moved/manifests/2.0.0") < 2 {
		t.Fatalf("requests = %v, want a challenge before the credential", server.requests)
	}
}

// The registry host comes out of the index, which is a document the repository
// serves. Sending the stored credential to whatever host it names would turn
// "this repository needs a password" into a way to collect one.
func TestOCICredentialGoesOnlyToTheConfiguredHost(t *testing.T) {
	t.Parallel()

	repository := store.HelmRepository{
		URL:      "https://charts.example.test/stable",
		Username: "robot",
		Password: "s3cret",
	}
	if ociCredential(repository, "registry.evil.test") != nil {
		t.Fatal("credential offered to a host the administrator never entered")
	}
	if ociCredential(repository, "charts.example.test") == nil {
		t.Fatal("credential withheld from the configured host")
	}
	// Nothing to send is not the same as refusing to send.
	if ociCredential(store.HelmRepository{URL: "https://charts.example.test"}, "charts.example.test") != nil {
		t.Fatal("a repository with no credential offered one")
	}
}

func TestOCIReferenceResolution(t *testing.T) {
	t.Parallel()

	target, tag, err := ociRepository("oci://registry.example.test/charts/demo:1.2.0", "9.9.9")
	if err != nil || tag != "1.2.0" {
		t.Fatalf("ociRepository() = %q, %v", tag, err)
	}
	if target.Reference.Registry != "registry.example.test" ||
		target.Reference.Repository != "charts/demo" {
		t.Fatalf("reference = %+v", target.Reference)
	}

	// An index is a document someone else wrote. A reference without a tag
	// falls back to the version that entry is published under rather than
	// failing: those are the same thing.
	_, tag, err = ociRepository("oci://registry.example.test/charts/demo", "1.2.0")
	if err != nil || tag != "1.2.0" {
		t.Fatalf("ociRepository(no tag) = %q, %v", tag, err)
	}
	if _, _, err = ociRepository("oci://registry.example.test/charts/demo", ""); err == nil {
		t.Fatal("ociRepository() accepted a reference naming no version")
	}
	if _, _, err = ociRepository("https://charts.example.test/demo.tgz", "1.0.0"); err == nil {
		t.Fatal("ociRepository() accepted a plain URL")
	}
}

// The chart is identified by Helm's media type rather than by position: a
// manifest also carries the config blob and may carry a provenance file.
func TestChartLayersAreFoundByMediaType(t *testing.T) {
	t.Parallel()

	manifest, err := json.Marshal(ocispec.Manifest{
		Layers: []ocispec.Descriptor{
			{MediaType: helmregistry.ProvLayerMediaType, Digest: "sha256:aa", Size: 1},
			{MediaType: helmregistry.ChartLayerMediaType, Digest: "sha256:bb", Size: 2},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	imageManifest := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest}
	layer, provenance, err := chartLayers(imageManifest, manifest, "oci://example.test/demo:1")
	if err != nil || layer.Digest != "sha256:bb" {
		t.Fatalf("chartLayers() = %+v, %v", layer, err)
	}
	// The provenance layer is found the same way and reported alongside, so a
	// signing policy has something to verify without a second manifest read.
	if provenance.Digest != "sha256:aa" {
		t.Fatalf("chartLayers() provenance = %+v", provenance)
	}

	// An artifact of some other kind is named as such rather than handed to the
	// chart loader to fail on.
	other, err := json.Marshal(ocispec.Manifest{
		Layers: []ocispec.Descriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := chartLayers(imageManifest, other, "oci://example.test/demo:1"); err == nil {
		t.Fatal("chartLayers() accepted a manifest with no chart layer")
	}

	// Charts are not built per platform, so an index is not one of them.
	if _, _, err := chartLayers(
		ocispec.Descriptor{MediaType: ocispec.MediaTypeImageIndex},
		manifest,
		"oci://example.test/demo:1",
	); err == nil {
		t.Fatal("chartLayers() accepted a multi-platform index")
	}
}

// The manifest states the size, so an archive too large to send to an Agent is
// refused before a byte of it is fetched.
func TestOCIChartTooLargeIsRefusedBeforeDownload(t *testing.T) {
	t.Parallel()

	server := newOCIRegistryServer(t, chartArchive(t, "moved", "2.0.0"))
	server.layerSize = 1 << 30
	service, _ := newTestService(t, ociRepositoryEntry(server.URL))

	_, err := service.GetChart(context.Background(), testRepositoryID, "moved", "")
	if !errors.Is(err, ErrChartTooLarge) {
		t.Fatalf("GetChart() = %v, want ErrChartTooLarge", err)
	}
	if pulls := server.fetched("/v2/charts/moved/blobs/" + server.chartLayer); pulls != 0 {
		t.Fatalf("blob was fetched %d times despite the size bound", pulls)
	}
}

// A tag the registry does not have is the same answer as a version missing from
// an index, not a broken registry: an operator sent to check the registry would
// find nothing wrong with it.
func TestOCIMissingTagIsReportedAsAMissingChart(t *testing.T) {
	t.Parallel()

	server := newOCIRegistryServer(t, chartArchive(t, "moved", "2.0.0"))
	service, _ := newTestService(t, ociRepositoryEntry(server.URL))

	_, _, err := service.fetchOCIChartArchive(
		context.Background(),
		ociRepositoryEntry(server.URL),
		"oci://"+strings.TrimPrefix(server.URL, "https://")+"/charts/moved",
		"7.7.7",
		1<<20,
		false,
	)
	if !errors.Is(err, ErrChartNotFound) {
		t.Fatalf("pull of an absent tag = %v, want ErrChartNotFound", err)
	}
}

// A chart published to a registry is verified from the provenance layer of the
// same manifest — the same policy, the same keys and the same refusals as on
// the HTTP path, because which protocol carried the archive is not a property
// an operator configured.
func TestOCIChartProvenanceIsVerified(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "moved", "2.0.0")
	entity, keyring := signingKey(t, "release-bot")
	server := newOCIRegistryServer(t, archive)
	server.publishProvenance(signProvenance(t, entity, "moved-2.0.0.tgz", archiveDigest(archive)))

	repository := ociRepositoryEntry(server.URL)
	repository.SignaturePolicy = string(SignatureRequired)
	repository.PublicKeyring = keyring
	service, _ := newTestService(t, repository)

	detail, err := service.GetChart(context.Background(), testRepositoryID, "moved", "")
	if err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if !detail.Signature.Verified || detail.Signature.FileName != "moved-2.0.0.tgz" {
		t.Fatalf("signature = %+v, want a verified one", detail.Signature)
	}
}

// A registry that publishes no provenance layer is the unsigned case, and under
// a policy that requires one the chart is refused — without the blob ever being
// asked for, because the manifest already said there was none.
func TestOCIChartWithoutProvenanceIsRefusedWhenRequired(t *testing.T) {
	t.Parallel()

	_, keyring := signingKey(t, "release-bot")
	server := newOCIRegistryServer(t, chartArchive(t, "moved", "2.0.0"))
	repository := ociRepositoryEntry(server.URL)
	repository.SignaturePolicy = string(SignatureRequired)
	repository.PublicKeyring = keyring
	service, _ := newTestService(t, repository)

	_, err := service.GetChart(context.Background(), testRepositoryID, "moved", "")
	if !errors.Is(err, ErrChartUnsigned) {
		t.Fatalf("GetChart() = %v, want ErrChartUnsigned", err)
	}
}

// Nothing is fetched to answer a question nobody asked: with verification off,
// the provenance layer named by the manifest is left alone.
func TestOCIProvenanceIsNotFetchedWhenNothingWillCheckIt(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "moved", "2.0.0")
	entity, _ := signingKey(t, "release-bot")
	server := newOCIRegistryServer(t, archive)
	server.publishProvenance(signProvenance(t, entity, "moved-2.0.0.tgz", archiveDigest(archive)))
	service, _ := newTestService(t, ociRepositoryEntry(server.URL))

	if _, err := service.GetChart(context.Background(), testRepositoryID, "moved", ""); err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if pulls := server.fetched("/v2/charts/moved/blobs/" + server.provLayer); pulls != 0 {
		t.Fatalf("provenance blob was fetched %d times under a disabled policy", pulls)
	}
}
