package helm

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	testRepositoryID = "3f1d8c5e-9a2b-4c7d-8e6f-1a2b3c4d5e6f"
	testClusterID    = "8c2f1b04-5d6e-4f70-a812-93b4c5d6e7f8"
)

// chartArchive builds a minimal but real Helm chart archive, so the loader and
// the catalogue are exercised against something Helm would actually accept
// rather than against a stub.
func chartArchive(t *testing.T, name string, version string) []byte {
	t.Helper()
	files := map[string]string{
		name + "/Chart.yaml": fmt.Sprintf(
			"apiVersion: v2\nname: %s\nversion: %s\nappVersion: \"1.4\"\ndescription: a test chart\n",
			name,
			version,
		),
		name + "/values.yaml": "# how many copies to run\nreplicaCount: 1\n",
		name + "/README.md":   "# " + name + "\n",
		name + "/templates/deployment.yaml": "apiVersion: apps/v1\nkind: Deployment\n" +
			"metadata:\n  name: {{ .Release.Name }}\n",
		name + "/templates/NOTES.txt": "Thanks for installing {{ .Chart.Name }}.\n",
		// A chart may package anything, so the fixture packages something that
		// is not text. The file browser has to notice on the bytes rather than
		// on the extension, which is why this one is named `.yaml`.
		name + "/files/logo.yaml": "\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR",
	}
	buffer := &bytes.Buffer{}
	compressor := gzip.NewWriter(buffer)
	archive := tar.NewWriter(compressor)
	for path, content := range files {
		if err := archive.WriteHeader(&tar.Header{
			Name: path,
			Mode: 0o644,
			Size: int64(len(content)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := archive.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// repositoryServer stands in for a chart repository. It records what it was
// asked for and whether the request carried a credential, because "the
// credential is sent upstream and never returned downstream" is a rule worth
// testing from both sides.
type repositoryServer struct {
	*httptest.Server
	requests  []string
	basicAuth []string
}

func newRepositoryServer(t *testing.T, archive []byte) *repositoryServer {
	t.Helper()
	server := &repositoryServer{}
	server.Server = httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		server.requests = append(server.requests, request.URL.Path)
		username, password, ok := request.BasicAuth()
		if ok {
			server.basicAuth = append(server.basicAuth, username+":"+password)
		}
		switch request.URL.Path {
		case "/index.yaml":
			fmt.Fprintf(writer, `apiVersion: v1
entries:
  demo:
    - name: demo
      version: 1.2.0
      appVersion: "1.4"
      description: a test chart
      keywords: [demo, testing]
      urls: [%s/charts/demo-1.2.0.tgz]
    - name: demo
      version: 1.1.0
      appVersion: "1.3"
      description: a test chart
      urls: [%s/charts/demo-1.1.0.tgz]
  other:
    - name: other
      version: 0.1.0
      description: something else
      urls: [%s/charts/other-0.1.0.tgz]
`, server.URL, server.URL, server.URL)
		case "/charts/demo-1.2.0.tgz", "/charts/demo-1.1.0.tgz":
			_, _ = writer.Write(archive)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// stubRepositoryStore is the catalogue without a database.
type stubRepositoryStore struct {
	repository store.HelmRepository
	deleted    bool
}

func (stub *stubRepositoryStore) ListHelmRepositories(
	context.Context,
) ([]store.HelmRepository, error) {
	return []store.HelmRepository{stub.repository}, nil
}

func (stub *stubRepositoryStore) GetHelmRepository(
	_ context.Context,
	id string,
) (store.HelmRepository, error) {
	if id != stub.repository.ID {
		return store.HelmRepository{}, store.ErrHelmRepositoryNotFound
	}
	// The public read never carries the password.
	public := stub.repository
	public.Password = ""
	return public, nil
}

func (stub *stubRepositoryStore) GetHelmRepositoryCredentials(
	_ context.Context,
	id string,
) (store.HelmRepository, error) {
	if id != stub.repository.ID {
		return store.HelmRepository{}, store.ErrHelmRepositoryNotFound
	}
	return stub.repository, nil
}

func (stub *stubRepositoryStore) CreateHelmRepository(
	_ context.Context,
	input store.CreateHelmRepositoryParams,
) (store.HelmRepository, error) {
	stub.repository = store.HelmRepository{
		ID:             input.ID,
		Name:           input.Name,
		URL:            input.URL,
		Username:       input.Username,
		Password:       input.Password,
		Enabled:        input.Enabled,
		HasCredentials: input.Password != "",
	}
	return stub.GetHelmRepository(context.Background(), input.ID)
}

func (stub *stubRepositoryStore) UpdateHelmRepository(
	_ context.Context,
	input store.UpdateHelmRepositoryParams,
) (store.HelmRepository, error) {
	if input.Password != nil {
		stub.repository.Password = *input.Password
	}
	stub.repository.Name = input.Name
	stub.repository.URL = input.URL
	stub.repository.Enabled = input.Enabled
	stub.repository.HasCredentials = stub.repository.Password != ""
	return stub.GetHelmRepository(context.Background(), input.ID)
}

func (stub *stubRepositoryStore) DeleteHelmRepository(_ context.Context, _ string) error {
	stub.deleted = true
	return nil
}

// recordingAgent captures what the Server decided to send to a Cluster.
type recordingAgent struct {
	request     *agentv1.HelmRequest
	values      []byte
	chart       []byte
	report      string
	failWith    *agentv1.HelmResponse
	transportEr error
}

func (agent *recordingAgent) RequestHelm(
	_ context.Context,
	_ string,
	request *agentv1.HelmRequest,
	values io.Reader,
	chart io.Reader,
	report io.Writer,
	_ string,
) (*agentv1.HelmResponse, error) {
	if agent.transportEr != nil {
		return nil, agent.transportEr
	}
	agent.request = request
	agent.values, _ = io.ReadAll(values)
	agent.chart, _ = io.ReadAll(chart)
	if agent.failWith != nil {
		return agent.failWith, nil
	}
	body := agent.report
	if body == "" {
		body = `{"name":"checkout","namespace":"shop","revision":1,"status":"deployed"}`
	}
	if _, err := io.WriteString(report, body); err != nil {
		return nil, err
	}
	return &agentv1.HelmResponse{
		Result:   agentv1.ResultCode_RESULT_CODE_OK,
		BodySize: uint64(len(body)),
	}, nil
}

func newTestService(t *testing.T, repository store.HelmRepository) (*Service, *recordingAgent) {
	t.Helper()
	agent := &recordingAgent{}
	service, err := NewService(
		&stubRepositoryStore{repository: repository},
		agent,
		"zke-server/test",
	)
	if err != nil {
		t.Fatal(err)
	}
	return service, agent
}

func testRepository(url string) store.HelmRepository {
	return store.HelmRepository{
		ID:      testRepositoryID,
		Name:    "demo",
		URL:     url,
		Enabled: true,
	}
}

func TestListChartsReadsTheIndex(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	page, err := service.ListCharts(context.Background(), testRepositoryID, "", 0)
	if err != nil {
		t.Fatalf("ListCharts() = %v", err)
	}
	if page.Total != 2 || len(page.Charts) != 2 {
		t.Fatalf("ListCharts() returned %d of %d charts, want 2 of 2", len(page.Charts), page.Total)
	}
	// Newest version per chart, which is the reduction `helm search` makes.
	if page.Charts[0].Name != "demo" || page.Charts[0].Version != "1.2.0" {
		t.Fatalf("first chart = %+v, want demo 1.2.0", page.Charts[0])
	}
	if page.Charts[0].VersionCount != 2 {
		t.Fatalf("demo version count = %d, want 2", page.Charts[0].VersionCount)
	}
	if page.FetchedAt.IsZero() {
		t.Fatal("ListCharts() did not report when the index was read")
	}
}

func TestListChartsFiltersBySearchTerm(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	page, err := service.ListCharts(context.Background(), testRepositoryID, "SOMETHING", 0)
	if err != nil {
		t.Fatalf("ListCharts() = %v", err)
	}
	if len(page.Charts) != 1 || page.Charts[0].Name != "other" {
		t.Fatalf("search matched %+v, want only `other`", page.Charts)
	}
	// The term matches the description here, so the total must reflect the
	// filter rather than the whole index.
	if page.Total != 1 {
		t.Fatalf("filtered total = %d, want 1", page.Total)
	}
}

// The index is cached, so browsing a repository does not refetch it for every
// keystroke of a search box.
func TestIndexIsCachedAndInvalidatedOnUpdate(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	for range 3 {
		if _, err := service.ListCharts(context.Background(), testRepositoryID, "", 0); err != nil {
			t.Fatal(err)
		}
	}
	indexReads := 0
	for _, path := range server.requests {
		if path == "/index.yaml" {
			indexReads++
		}
	}
	if indexReads != 1 {
		t.Fatalf("index was read %d times, want 1", indexReads)
	}
	// Correcting a mistyped URL must not leave the old answer in place.
	service.catalogue.forget(testRepositoryID)
	if _, err := service.ListCharts(context.Background(), testRepositoryID, "", 0); err != nil {
		t.Fatal(err)
	}
	indexReads = 0
	for _, path := range server.requests {
		if path == "/index.yaml" {
			indexReads++
		}
	}
	if indexReads != 2 {
		t.Fatalf("index was read %d times after invalidation, want 2", indexReads)
	}
}

func TestGetChartReadsTheChartsOwnDocuments(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	detail, err := service.GetChart(context.Background(), testRepositoryID, "demo", "")
	if err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if detail.Version != "1.2.0" || detail.AppVersion != "1.4" {
		t.Fatalf("GetChart() = %+v, want 1.2.0/1.4", detail)
	}
	// values.yaml is returned verbatim: its comments are the documentation for
	// half of what is in it, and a parse would drop them.
	if !strings.Contains(detail.Values, "# how many copies to run") {
		t.Fatalf("values lost their comments: %q", detail.Values)
	}
	if !strings.Contains(detail.README, "# demo") {
		t.Fatalf("README = %q", detail.README)
	}
}

func TestGetChartReportsAMissingVersion(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	if _, err := service.GetChart(
		context.Background(), testRepositoryID, "demo", "9.9.9",
	); !errors.Is(err, ErrChartNotFound) {
		t.Fatalf("GetChart() = %v, want ErrChartNotFound", err)
	}
	if _, err := service.GetChart(
		context.Background(), testRepositoryID, "nothing", "",
	); !errors.Is(err, ErrChartNotFound) {
		t.Fatalf("GetChart() for an unknown chart = %v, want ErrChartNotFound", err)
	}
}

// A disabled repository is not the same as a missing one: only one of them is
// fixed by an administrator turning something back on.
func TestDisabledRepositoryIsReportedSeparately(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	repository := testRepository(server.URL)
	repository.Enabled = false
	service, _ := newTestService(t, repository)

	if _, err := service.ListCharts(
		context.Background(), testRepositoryID, "", 0,
	); !errors.Is(err, ErrRepositoryDisabled) {
		t.Fatalf("ListCharts() = %v, want ErrRepositoryDisabled", err)
	}
}

// The credential is sent upstream as a header and never returned by any read.
func TestRepositoryCredentialTravelsUpstreamOnly(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	repository := testRepository(server.URL)
	repository.Username = "reader"
	repository.Password = "s3cret"
	repository.HasCredentials = true
	service, _ := newTestService(t, repository)

	if _, err := service.ListCharts(context.Background(), testRepositoryID, "", 0); err != nil {
		t.Fatal(err)
	}
	if len(server.basicAuth) == 0 || server.basicAuth[0] != "reader:s3cret" {
		t.Fatalf("repository saw %v, want the stored credential", server.basicAuth)
	}
	public, err := service.GetRepository(context.Background(), testRepositoryID)
	if err != nil {
		t.Fatal(err)
	}
	if !public.HasCredentials {
		t.Fatal("GetRepository() does not report that a credential is stored")
	}
	rendered := fmt.Sprintf("%+v", public)
	if strings.Contains(rendered, "s3cret") {
		t.Fatalf("GetRepository() returned the credential: %s", rendered)
	}
}

func TestInstallSendsTheChartAndValuesToTheAgent(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "demo", "1.2.0")
	server := newRepositoryServer(t, archive)
	service, agent := newTestService(t, testRepository(server.URL))

	report, err := service.Install(context.Background(), InstallInput{
		ClusterID:          testClusterID,
		Namespace:          "shop",
		Name:               "checkout",
		RepositoryID:       testRepositoryID,
		Chart:              "demo",
		Values:             "replicaCount: 3\n",
		CreateNamespace:    true,
		Wait:               true,
		TimeoutSeconds:     120,
		Description:        "alice",
		AllowClusterScoped: true,
	})
	if err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if report.Name != "checkout" || report.Revision != 1 {
		t.Fatalf("Install() report = %+v", report)
	}
	if agent.request.GetAction() != agentv1.HelmAction_HELM_ACTION_INSTALL {
		t.Fatalf("action = %v", agent.request.GetAction())
	}
	// The archive is forwarded byte for byte: this Server never repacks a
	// chart, so what runs in the Cluster is what the repository published.
	if !bytes.Equal(agent.chart, archive) {
		t.Fatal("the chart the Agent received is not the one the repository published")
	}
	if string(agent.values) != "replicaCount: 3" {
		t.Fatalf("values = %q", agent.values)
	}
	if !agent.request.GetCreateNamespace() || !agent.request.GetWait() ||
		agent.request.GetTimeoutSeconds() != 120 ||
		!agent.request.GetAllowClusterScoped() {
		t.Fatalf("request lost its switches: %+v", agent.request)
	}
}

// AllowClusterScoped is the caller's decision from the operator's permissions.
// A service that defaulted it to true would hand every install the power to
// change the whole Cluster.
func TestInstallDefaultsClusterScopedToRefused(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))

	if _, err := service.Install(context.Background(), InstallInput{
		ClusterID:    testClusterID,
		Namespace:    "shop",
		Name:         "checkout",
		RepositoryID: testRepositoryID,
		Chart:        "demo",
	}); err != nil {
		t.Fatal(err)
	}
	if agent.request.GetAllowClusterScoped() {
		t.Fatal("Install() allowed cluster-scoped objects without being told to")
	}
}

func TestUpgradeRefusesContradictoryValueModes(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	_, err := service.Upgrade(context.Background(), UpgradeInput{
		InstallInput: InstallInput{
			ClusterID:    testClusterID,
			Namespace:    "shop",
			Name:         "checkout",
			RepositoryID: testRepositoryID,
			Chart:        "demo",
		},
		ResetValues: true,
		ReuseValues: true,
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Upgrade() = %v, want ErrInvalidInput", err)
	}
}

// A rollback replays stored history, so it must never carry a chart: sending
// one would mean the caller believes it is choosing the content.
func TestRollbackSendsNoChart(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))

	if _, err := service.Rollback(context.Background(), RollbackInput{
		ClusterID: testClusterID,
		Namespace: "shop",
		Name:      "checkout",
		Revision:  2,
	}); err != nil {
		t.Fatalf("Rollback() = %v", err)
	}
	if agent.request.GetChartSize() != 0 || agent.request.GetValuesSize() != 0 {
		t.Fatalf("rollback carried content: %+v", agent.request)
	}
	if agent.request.GetRevision() != 2 {
		t.Fatalf("revision = %d, want 2", agent.request.GetRevision())
	}
}

func TestUninstallCarriesKeepHistory(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))

	if _, err := service.Uninstall(context.Background(), UninstallInput{
		ClusterID:   testClusterID,
		Namespace:   "shop",
		Name:        "checkout",
		KeepHistory: true,
	}); err != nil {
		t.Fatalf("Uninstall() = %v", err)
	}
	if !agent.request.GetKeepHistory() {
		t.Fatal("uninstall lost keep_history")
	}
}

// A successful uninstall that kept no history leaves no revision to report. The
// Server answers with the request's own identity rather than with nothing.
func TestUninstallWithoutAReportStillIdentifiesTheRelease(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))
	agent.report = " "
	agent.failWith = &agentv1.HelmResponse{Result: agentv1.ResultCode_RESULT_CODE_OK}

	report, err := service.Uninstall(context.Background(), UninstallInput{
		ClusterID: testClusterID,
		Namespace: "shop",
		Name:      "checkout",
	})
	if err != nil {
		t.Fatalf("Uninstall() = %v", err)
	}
	if report.Name != "checkout" || report.Namespace != "shop" || !report.Deleted {
		t.Fatalf("Uninstall() report = %+v", report)
	}
}

// A refusal keeps the Cluster's own words: the operator has to read which rule
// they hit, not that "the operation failed".
func TestRejectionKeepsTheClustersReason(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))
	agent.failWith = &agentv1.HelmResponse{
		Result:               agentv1.ResultCode_RESULT_CODE_FORBIDDEN,
		KubernetesStatusCode: http.StatusForbidden,
		Reason:               "HelmChartCrossNamespace",
		Message:              "chart renders ConfigMap/stolen into Namespace \"kube-system\"",
	}

	_, err := service.Install(context.Background(), InstallInput{
		ClusterID:    testClusterID,
		Namespace:    "shop",
		Name:         "checkout",
		RepositoryID: testRepositoryID,
		Chart:        "demo",
	})
	if !errors.Is(err, ErrReleaseRejected) {
		t.Fatalf("Install() = %v, want ErrReleaseRejected", err)
	}
	var rejection *ReleaseRejection
	if !errors.As(err, &rejection) {
		t.Fatalf("Install() = %v, want a ReleaseRejection", err)
	}
	if rejection.Reason != "HelmChartCrossNamespace" ||
		!strings.Contains(rejection.Message, "kube-system") {
		t.Fatalf("rejection = %+v", rejection)
	}
}

func TestReleaseNameFollowsHelmsOwnRule(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	_, err := service.Install(context.Background(), InstallInput{
		ClusterID:    testClusterID,
		Namespace:    "shop",
		Name:         strings.Repeat("a", 54),
		RepositoryID: testRepositoryID,
		Chart:        "demo",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Install() with a 54-character name = %v, want ErrInvalidInput", err)
	}
}

func TestValuesMustBeAMapping(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	_, err := service.Install(context.Background(), InstallInput{
		ClusterID:    testClusterID,
		Namespace:    "shop",
		Name:         "checkout",
		RepositoryID: testRepositoryID,
		Chart:        "demo",
		Values:       "- one\n- two\n",
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Install() with a list of values = %v, want ErrInvalidInput", err)
	}
}

func TestRepositoryInputValidation(t *testing.T) {
	t.Parallel()

	cases := map[string]RepositoryInput{
		"no name":            {Name: "  ", URL: "https://charts.example.invalid"},
		"relative URL":       {Name: "demo", URL: "charts.example.invalid"},
		"unsupported scheme": {Name: "demo", URL: "ftp://charts.example.invalid"},
		"credentials in URL": {Name: "demo", URL: "https://user:pass@charts.example.invalid"},
		"malformed CA":       {Name: "demo", URL: "https://charts.example.invalid", CACertificatePEM: "not a certificate"},
	}
	for name, input := range cases {
		if _, err := normalizeRepositoryInput(input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: normalizeRepositoryInput() = %v, want ErrInvalidInput", name, err)
		}
	}
	normalized, err := normalizeRepositoryInput(RepositoryInput{
		Name: "  demo  ",
		URL:  "https://charts.example.invalid/stable/",
	})
	if err != nil {
		t.Fatalf("normalizeRepositoryInput() = %v", err)
	}
	if normalized.Name != "demo" {
		t.Fatalf("name = %q, want it trimmed", normalized.Name)
	}
	// The trailing slash comes off so `URL + "/index.yaml"` is one path rather
	// than two joined by an empty segment.
	if normalized.URL != "https://charts.example.invalid/stable" {
		t.Fatalf("url = %q", normalized.URL)
	}
}

// An upstream failure says what the repository answered: "could not be read" on
// its own would send an administrator looking at ZKE rather than at the
// repository they just configured.
func TestUnreachableRepositoryExplainsItself(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	service, _ := newTestService(t, testRepository(server.URL))

	_, err := service.ListCharts(context.Background(), testRepositoryID, "", 0)
	if !errors.Is(err, ErrRepositoryUnreachable) {
		t.Fatalf("ListCharts() = %v, want ErrRepositoryUnreachable", err)
	}
	var detailed interface{ Detail() string }
	if !errors.As(err, &detailed) || !strings.Contains(detailed.Detail(), "401") {
		t.Fatalf("error does not report what the repository answered: %v", err)
	}
}

// A Helm 2 index carries a different entry shape. Reading it half-way would
// produce a catalogue whose versions do not resolve, so it is refused.
func TestHelm2IndexIsRefused(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		_ *http.Request,
	) {
		fmt.Fprint(writer, "entries:\n  demo: []\n")
	}))
	t.Cleanup(server.Close)
	service, _ := newTestService(t, testRepository(server.URL))

	if _, err := service.ListCharts(
		context.Background(), testRepositoryID, "", 0,
	); !errors.Is(err, ErrRepositoryUnreachable) {
		t.Fatalf("ListCharts() = %v, want a refusal", err)
	}
}

func TestServiceRequiresItsDependencies(t *testing.T) {
	t.Parallel()

	if _, err := NewService(nil, &recordingAgent{}, ""); err == nil {
		t.Fatal("NewService() accepted a nil store")
	}
	if _, err := NewService(&stubRepositoryStore{}, nil, ""); err == nil {
		t.Fatal("NewService() accepted a nil Agent access")
	}
}
