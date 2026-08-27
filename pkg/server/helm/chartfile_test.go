package helm

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func chartDownloads(server *repositoryServer) int {
	count := 0
	for _, path := range server.requests {
		if strings.HasPrefix(path, "/charts/") {
			count++
		}
	}
	return count
}

// The file listing travels with the chart detail, because that request already
// downloaded and parsed the archive.
func TestGetChartListsEveryFileInTheArchive(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	detail, err := service.GetChart(context.Background(), testRepositoryID, "demo", "")
	if err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	paths := make([]string, 0, len(detail.Files))
	for _, file := range detail.Files {
		paths = append(paths, file.Path)
	}
	// Sorted, so the browser's tree does not depend on the order the archive
	// happened to be written in.
	want := []string{
		"Chart.yaml",
		"README.md",
		"files/logo.yaml",
		"templates/NOTES.txt",
		"templates/deployment.yaml",
		"values.yaml",
	}
	if strings.Join(paths, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", paths, want)
	}
	if detail.FileCount != len(want) || detail.FilesTruncated {
		t.Fatalf("file count = %d truncated = %v, want %d false",
			detail.FileCount, detail.FilesTruncated, len(want))
	}
	for _, file := range detail.Files {
		// The binary member is named `.yaml`: the decision has to be made on
		// the bytes, or a chart could hand the browser anything it liked.
		wantText := file.Path != "files/logo.yaml"
		if file.Text != wantText {
			t.Fatalf("%s text = %v, want %v", file.Path, file.Text, wantText)
		}
		if file.Size == 0 {
			t.Fatalf("%s reported no size", file.Path)
		}
	}
}

func TestGetChartFileReturnsTheFileVerbatim(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	file, err := service.GetChartFile(
		context.Background(), testRepositoryID, "demo", "", "templates/deployment.yaml",
	)
	if err != nil {
		t.Fatalf("GetChartFile() = %v", err)
	}
	if !strings.Contains(file.Content, "{{ .Release.Name }}") {
		t.Fatalf("content = %q, want the template unrendered", file.Content)
	}
	if !file.Text || file.Truncated {
		t.Fatalf("file = %+v, want text and untruncated", file)
	}
	if file.Size != len(file.Content) {
		t.Fatalf("size = %d, want %d", file.Size, len(file.Content))
	}
	// The version is reported as it resolved, not as it was asked for.
	if file.Version != "1.2.0" {
		t.Fatalf("version = %q, want 1.2.0", file.Version)
	}
}

// A file that is not text is reported as present but not shown: there is
// nothing useful to put in a code viewer, and Size still says it is there.
func TestGetChartFileWithholdsBinaryContent(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	file, err := service.GetChartFile(
		context.Background(), testRepositoryID, "demo", "", "files/logo.yaml",
	)
	if err != nil {
		t.Fatalf("GetChartFile() = %v", err)
	}
	if file.Text || file.Content != "" {
		t.Fatalf("binary file = %+v, want text=false and no content", file)
	}
	if file.Size == 0 {
		t.Fatal("binary file reported no size")
	}
}

// The path is matched against the archive's own member names, so nothing a
// caller writes is ever joined onto a filesystem path.
func TestGetChartFileRejectsPathsTheArchiveDoesNotHold(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	for _, path := range []string{
		"../../etc/passwd",
		"/etc/passwd",
		"templates/",
		"templates/deployment.yml",
	} {
		_, err := service.GetChartFile(context.Background(), testRepositoryID, "demo", "", path)
		if !errors.Is(err, ErrChartFileNotFound) {
			t.Fatalf("GetChartFile(%q) = %v, want ErrChartFileNotFound", path, err)
		}
	}
	if _, err := service.GetChartFile(
		context.Background(), testRepositoryID, "demo", "", "   ",
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetChartFile(blank) = %v, want ErrInvalidInput", err)
	}
	if _, err := service.GetChartFile(
		context.Background(), testRepositoryID, "demo", "", strings.Repeat("a", 2000),
	); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("GetChartFile(overlong) = %v, want ErrInvalidInput", err)
	}
}

// Browsing a chart is a sequence of requests about one archive. Downloading it
// again for every file opened would put that cost on somebody else's server.
func TestChartArchiveIsDownloadedOncePerBrowsingSession(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, _ := newTestService(t, testRepository(server.URL))

	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"Chart.yaml", "values.yaml", "templates/deployment.yaml",
	} {
		if _, err := service.GetChartFile(
			context.Background(), testRepositoryID, "demo", "", path,
		); err != nil {
			t.Fatal(err)
		}
	}
	if downloads := chartDownloads(server); downloads != 1 {
		t.Fatalf("chart was downloaded %d times, want 1", downloads)
	}

	// The detail asked for "newest"; the file reads that follow it ask for the
	// version the detail reported. Both are the same archive, so both must be
	// the same cache entry — otherwise the first click after opening a chart
	// downloads it a second time.
	if _, err := service.GetChartFile(
		context.Background(), testRepositoryID, "demo", "1.2.0", "values.yaml",
	); err != nil {
		t.Fatal(err)
	}
	if downloads := chartDownloads(server); downloads != 1 {
		t.Fatalf("resolved version downloaded the chart again: %d downloads", downloads)
	}

	// A repository that was edited points somewhere else or authenticates
	// differently, so what it published a moment ago is no longer an answer to
	// the same question.
	service.forgetRepository(testRepositoryID)
	if _, err := service.GetChartFile(
		context.Background(), testRepositoryID, "demo", "", "values.yaml",
	); err != nil {
		t.Fatal(err)
	}
	if downloads := chartDownloads(server); downloads != 2 {
		t.Fatalf("chart was downloaded %d times after invalidation, want 2", downloads)
	}
}

// A release write fetches the bytes it is about to apply rather than a copy the
// catalogue happened to still be holding.
func TestReleaseWriteDoesNotUseTheChartCache(t *testing.T) {
	t.Parallel()

	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, testRepository(server.URL))

	if _, err := service.GetChart(context.Background(), testRepositoryID, "demo", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Install(context.Background(), InstallInput{
		ClusterID:    testClusterID,
		Namespace:    "shop",
		Name:         "checkout",
		RepositoryID: testRepositoryID,
		Chart:        "demo",
	}); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if len(agent.chart) == 0 {
		t.Fatal("Install() sent no chart archive")
	}
	if downloads := chartDownloads(server); downloads != 2 {
		t.Fatalf("chart was downloaded %d times, want 2", downloads)
	}
}

func TestIsTextFile(t *testing.T) {
	t.Parallel()

	cases := map[string]struct {
		data []byte
		want bool
	}{
		"empty":            {[]byte{}, true},
		"ascii":            {[]byte("replicaCount: 1\n"), true},
		"utf8":             {[]byte("描述: 一个 Chart\n"), true},
		"nul byte":         {[]byte("ok\x00then"), false},
		"invalid utf8":     {[]byte{0xff, 0xfe, 0xfd}, false},
		"long utf8":        {[]byte(strings.Repeat("好", 20000)), true},
		"binary past head": {append([]byte(strings.Repeat("a", 9000)), 0x00), true},
	}
	for name, testCase := range cases {
		if got := isTextFile(testCase.data); got != testCase.want {
			t.Errorf("isTextFile(%s) = %v, want %v", name, got, testCase.want)
		}
	}
}
