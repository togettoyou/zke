package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/metricslibrary"
)

const testHTTPProjectID = "00000000-0000-4000-8000-0000000000a1"

type fakeSavedQueryService struct {
	list   func(context.Context, string, string) ([]metricslibrary.SavedQuery, error)
	create func(context.Context, string, string, metricslibrary.Input) (metricslibrary.SavedQuery, error)
	update func(context.Context, string, string, string, metricslibrary.Input) (metricslibrary.SavedQuery, error)
	remove func(context.Context, string, string, string) (metricslibrary.SavedQuery, error)
}

func (service *fakeSavedQueryService) List(
	ctx context.Context, projectID, userID string,
) ([]metricslibrary.SavedQuery, error) {
	return service.list(ctx, projectID, userID)
}

func (service *fakeSavedQueryService) Create(
	ctx context.Context, projectID, userID string, input metricslibrary.Input,
) (metricslibrary.SavedQuery, error) {
	return service.create(ctx, projectID, userID, input)
}

func (service *fakeSavedQueryService) Update(
	ctx context.Context, projectID, userID, id string, input metricslibrary.Input,
) (metricslibrary.SavedQuery, error) {
	return service.update(ctx, projectID, userID, id, input)
}

func (service *fakeSavedQueryService) Delete(
	ctx context.Context, projectID, userID, id string,
) (metricslibrary.SavedQuery, error) {
	return service.remove(ctx, projectID, userID, id)
}

func savedQueryTestRouter(service metricsSavedQueryService) http.Handler {
	configureGinMode.Do(func() { gin.SetMode(gin.ReleaseMode) })
	router := gin.New()
	handler := newMetricsSavedQueryHandler(discardLogger(), service, nil, 5*time.Second)
	base := "/api/v1/projects/:project_id/metrics/saved-queries"
	router.GET(base, handler.list)
	router.POST(base, handler.create)
	router.PUT(base+"/:saved_query_id", handler.update)
	router.DELETE(base+"/:saved_query_id", handler.remove)
	return router
}

func savedQueryPath(suffix string) string {
	return "/api/v1/projects/" + testHTTPProjectID + "/metrics/saved-queries" + suffix
}

func TestSavedQueryHandlerReturnsWhatTheReaderMayEdit(t *testing.T) {
	t.Parallel()

	service := &fakeSavedQueryService{list: func(
		_ context.Context, projectID, _ string,
	) ([]metricslibrary.SavedQuery, error) {
		if projectID != testHTTPProjectID {
			t.Fatalf("project ID = %q", projectID)
		}
		return []metricslibrary.SavedQuery{{
			ID:               "00000000-0000-4000-8000-0000000000b1",
			ProjectID:        projectID,
			OwnerDisplayName: "Operator",
			Visibility:       metricslibrary.VisibilityProject,
			Name:             "内存用量",
			Expression:       "up",
			Editable:         false,
		}}, nil
	}}
	response := httptest.NewRecorder()
	savedQueryTestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, savedQueryPath(""), nil),
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	var result metricsSavedQueryListResponse
	if err := decodeSuccessResponse(response, &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Queries) != 1 || result.Queries[0].Editable {
		t.Fatalf("queries = %+v", result.Queries)
	}
	if result.Limit != metricslibrary.MaxPerProject {
		t.Fatalf("limit = %d", result.Limit)
	}
}

// detailedInvalid is the shape metricslibrary.InputError has: a refusal that
// already says what is wrong with the submission. The library's own tests check
// that it produces one; this checks that the HTTP layer passes the reason on
// instead of replacing it with its own fixed message.
type detailedInvalid struct{ detail string }

func (err detailedInvalid) Error() string  { return "invalid: " + err.detail }
func (err detailedInvalid) Unwrap() error  { return metricslibrary.ErrInvalidInput }
func (err detailedInvalid) Detail() string { return err.detail }

// The service's own reason reaches the author. An editor told only
// "invalid_request" leaves them guessing which of four fields it meant.
func TestSavedQueryHandlerReturnsTheValidationReason(t *testing.T) {
	t.Parallel()

	service := &fakeSavedQueryService{create: func(
		context.Context, string, string, metricslibrary.Input,
	) (metricslibrary.SavedQuery, error) {
		return metricslibrary.SavedQuery{}, detailedInvalid{
			detail: "MetricsQL 解析失败：unexpected token",
		}
	}}
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		savedQueryPath(""),
		strings.NewReader(`{"name":"a","expression":"sum by (node","visibility":"private"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	savedQueryTestRouter(service).ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	if !strings.Contains(response.Body.String(), "MetricsQL") {
		t.Errorf("the reason did not reach the caller: %s", response.Body)
	}
}

func TestSavedQueryHandlerMapsServiceFailures(t *testing.T) {
	t.Parallel()

	cases := []struct {
		err    error
		status int
		code   string
	}{
		{metricslibrary.ErrDenied, http.StatusForbidden, "forbidden"},
		{metricslibrary.ErrNotFound, http.StatusNotFound, "not_found"},
		{metricslibrary.ErrConflict, http.StatusConflict, "conflict"},
		{metricslibrary.ErrLimit, http.StatusConflict, "limit_reached"},
		{metricslibrary.ErrInvalidInput, http.StatusBadRequest, "invalid_request"},
	}
	for _, testCase := range cases {
		service := &fakeSavedQueryService{create: func(
			context.Context, string, string, metricslibrary.Input,
		) (metricslibrary.SavedQuery, error) {
			return metricslibrary.SavedQuery{}, testCase.err
		}}
		response := httptest.NewRecorder()
		request := httptest.NewRequest(
			http.MethodPost,
			savedQueryPath(""),
			strings.NewReader(`{"name":"a","expression":"up","visibility":"private"}`),
		)
		request.Header.Set("Content-Type", "application/json")
		savedQueryTestRouter(service).ServeHTTP(response, request)
		if response.Code != testCase.status {
			t.Errorf("%v: status = %d: %s", testCase.err, response.Code, response.Body)
			continue
		}
		assertErrorCode(t, response, testCase.code)
	}
}

func TestSavedQueryHandlerDeletesWithoutABody(t *testing.T) {
	t.Parallel()

	service := &fakeSavedQueryService{remove: func(
		_ context.Context, _, _, id string,
	) (metricslibrary.SavedQuery, error) {
		if id != "00000000-0000-4000-8000-0000000000b1" {
			t.Fatalf("id = %q", id)
		}
		return metricslibrary.SavedQuery{ID: id, Name: "内存用量"}, nil
	}}
	response := httptest.NewRecorder()
	savedQueryTestRouter(service).ServeHTTP(
		response,
		httptest.NewRequest(
			http.MethodDelete,
			savedQueryPath("/00000000-0000-4000-8000-0000000000b1"),
			nil,
		),
	)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
}

// A deployment with no metrics storage keeps the routes and reports the state.
func TestSavedQueryHandlerReportsDisabledMetrics(t *testing.T) {
	t.Parallel()

	response := httptest.NewRecorder()
	savedQueryTestRouter(metricsSavedQueryServiceOrNil(nil)).ServeHTTP(
		response,
		httptest.NewRequest(http.MethodGet, savedQueryPath(""), nil),
	)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "metrics_disabled")
}
