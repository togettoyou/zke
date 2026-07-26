package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestParseAndApplyListQuery(t *testing.T) {
	t.Parallel()
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodGet,
		"/items?limit=2&offset=1&q=Beta&status=active",
		nil,
	)
	query, err := parseListQuery(context)
	if err != nil {
		t.Fatal(err)
	}
	if query.Limit != 2 || query.Offset != 1 ||
		query.Search != "Beta" || query.Status != "active" {
		t.Fatalf("unexpected list query: %+v", query)
	}
	items, metadata := paginate([]string{"a", "b", "c", "d"}, query)
	if len(items) != 2 || items[0] != "b" || items[1] != "c" {
		t.Fatalf("unexpected page: %+v", items)
	}
	if metadata.Total != 4 || !metadata.HasMore {
		t.Fatalf("unexpected pagination: %+v", metadata)
	}
}

func TestParseListQueryRejectsInvalidBounds(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"/items?limit=0",
		"/items?limit=101",
		"/items?offset=-1",
		"/items?offset=1000001",
	} {
		response := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(response)
		context.Request = httptest.NewRequest(http.MethodGet, target, nil)
		if _, err := parseListQuery(context); err == nil {
			t.Errorf("parseListQuery(%q) succeeded", target)
		}
	}
}
