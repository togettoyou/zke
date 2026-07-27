package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

func listQueryContext(target string) *gin.Context {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(http.MethodGet, target, nil)
	return context
}

func TestParseListQuery(t *testing.T) {
	t.Parallel()

	query, err := parseListQuery(
		listQueryContext("/items?limit=2&offset=1&q=Beta&status=active"),
		listFilters{search: true, status: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if query.Page.Limit != 2 || query.Page.Offset != 1 ||
		query.Search != "Beta" || query.Status != "active" {
		t.Fatalf("unexpected list query: %+v", query)
	}
}

func TestParseListQueryAppliesDefaultPage(t *testing.T) {
	t.Parallel()

	query, err := parseListQuery(listQueryContext("/items"), listFilters{})
	if err != nil {
		t.Fatal(err)
	}
	if query.Page.Limit != pagination.DefaultLimit || query.Page.Offset != 0 {
		t.Fatalf("unexpected default page: %+v", query.Page)
	}
}

func TestParseListQueryRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/items?limit=0",
		"/items?limit=101",
		"/items?limit=abc",
		"/items?offset=-1",
		"/items?offset=1000001",
		"/items?offset=abc",
	} {
		if _, err := parseListQuery(listQueryContext(target), listFilters{}); err == nil {
			t.Errorf("parseListQuery(%q) succeeded", target)
		}
	}
}

// An endpoint must refuse a filter it does not implement rather than accept
// the request and silently return unfiltered rows.
func TestParseListQueryRejectsUnsupportedFilters(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		target    string
		supported listFilters
	}{
		{"/items?role=admin", listFilters{search: true, status: true}},
		{"/items?scope_type=global", listFilters{search: true, status: true}},
		{"/items?status=active", listFilters{search: true}},
		{"/items?q=term", listFilters{status: true}},
	} {
		if _, err := parseListQuery(
			listQueryContext(testCase.target),
			testCase.supported,
		); err == nil {
			t.Errorf("parseListQuery(%q) accepted an unsupported filter", testCase.target)
		}
	}
}

// An unknown parameter is a caller mistake, and answering it with an
// unfiltered page hides that mistake behind a plausible-looking result.
func TestParseListQueryRejectsUnknownParameters(t *testing.T) {
	t.Parallel()

	for _, target := range []string{
		"/items?unknown=1",
		"/items?limit=2&typo_status=active",
		"/items?actor_type=user",
	} {
		if _, err := parseListQuery(
			listQueryContext(target),
			listFilters{search: true, status: true},
		); err == nil {
			t.Errorf("parseListQuery(%q) accepted an unknown parameter", target)
		}
	}
}

// Endpoint-specific filters go through the same declaration, so the audit
// trail's selectors are parsed rather than read straight off the request.
func TestParseListQueryReadsDeclaredExtraFilters(t *testing.T) {
	t.Parallel()

	query, err := parseListQuery(
		listQueryContext("/audit-events?actor_type=user&result=denied"),
		listFilters{extra: auditQueryFilters},
	)
	if err != nil {
		t.Fatal(err)
	}
	if query.Filter("actor_type") != "user" || query.Filter("result") != "denied" {
		t.Fatalf("unexpected audit filters: %+v", query)
	}
	if query.Filter("action") != "" {
		t.Fatalf("absent filter reported a value: %q", query.Filter("action"))
	}
	if _, err := parseListQuery(
		listQueryContext("/audit-events?q=term"),
		listFilters{extra: auditQueryFilters},
	); err == nil {
		t.Fatal("audit query accepted a search filter it does not implement")
	}
}

func TestResponsePagination(t *testing.T) {
	t.Parallel()

	metadata := responsePagination(pagination.Result{
		Limit:   10,
		Offset:  20,
		Total:   35,
		HasMore: true,
	})
	if metadata.Limit != 10 || metadata.Offset != 20 ||
		metadata.Total != 35 || !metadata.HasMore {
		t.Fatalf("unexpected pagination metadata: %+v", metadata)
	}
}
