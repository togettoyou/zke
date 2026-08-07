package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/kubernetesmanifest"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

const testManifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: settings\n"

// The resource layer decides which permission a document answers to but must not
// know what it is called. This mapping is the only place the two vocabularies
// meet, so a requirement added there without a name here would reach the Console
// as an empty string — a document whose refusal names no permission.
func TestManifestRequirementsAreAllPublishedAsPermissions(t *testing.T) {
	t.Parallel()

	requirements := []kubernetesresource.ManifestRequirement{
		kubernetesresource.ManifestRequirementResourceCreate,
		kubernetesresource.ManifestRequirementResourceUpdate,
		kubernetesresource.ManifestRequirementResourceDelete,
		kubernetesresource.ManifestRequirementNamespaceManage,
		kubernetesresource.ManifestRequirementSecretManage,
		kubernetesresource.ManifestRequirementRBACManage,
	}
	seen := make(map[string]kubernetesresource.ManifestRequirement, len(requirements))
	for _, requirement := range requirements {
		permission := manifestRequirementPermission(requirement)
		if permission == "" {
			t.Errorf("requirement %q maps to no permission name", requirement)
			continue
		}
		if previous, exists := seen[permission]; exists {
			t.Errorf(
				"requirements %q and %q both map to %q",
				previous, requirement, permission,
			)
		}
		seen[permission] = requirement
	}
}

func TestKubernetesManifestApplyReportsEachDocument(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesManifestService{
		execute: func(
			_ context.Context,
			_ kubernetesmanifest.ResourceAccess,
			input kubernetesmanifest.Input,
		) (kubernetesmanifest.Result, error) {
			if input.Operation != kubernetesmanifest.OperationApply ||
				input.ClusterID != testHTTPClusterID ||
				input.Namespace != "team-a" ||
				!input.DryRun || input.Confirm ||
				string(input.Manifest) != testManifest {
				t.Fatalf("unexpected manifest input: %+v", input)
			}
			return kubernetesmanifest.Result{
				DryRun:  true,
				Allowed: true,
				Documents: []kubernetesmanifest.Document{{
					Index:       0,
					APIVersion:  "v1",
					Kind:        "ConfigMap",
					Namespace:   "team-a",
					Name:        "settings",
					Action:      kubernetesmanifest.ActionCreate,
					Status:      kubernetesmanifest.StatusSucceeded,
					Requirement: kubernetesresource.ManifestRequirementResourceCreate,
				}},
			}, nil
		},
	}
	response := manifestRequest(
		t, service, "apply", "?namespace=team-a&dry_run=true", testManifest,
	)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", response.Code, response.Body)
	}
	var body struct {
		Data struct {
			DryRun    bool `json:"dry_run"`
			Allowed   bool `json:"allowed"`
			Documents []struct {
				Kind       string `json:"kind"`
				Action     string `json:"action"`
				Status     string `json:"status"`
				Permission string `json:"permission"`
			} `json:"documents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !body.Data.DryRun || !body.Data.Allowed || len(body.Data.Documents) != 1 {
		t.Fatalf("unexpected response: %s", response.Body)
	}
	document := body.Data.Documents[0]
	if document.Kind != "ConfigMap" || document.Action != "create" ||
		document.Status != "succeeded" ||
		document.Permission != "cluster.resource.create" {
		t.Fatalf("unexpected document: %+v", document)
	}
}

// A manifest with one document the caller may not write is refused whole, and
// the response says so with a status rather than a partial result an operator
// might read as success.
func TestKubernetesManifestRefusalIsForbiddenAndWritesNothing(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesManifestService{
		execute: func(
			context.Context,
			kubernetesmanifest.ResourceAccess,
			kubernetesmanifest.Input,
		) (kubernetesmanifest.Result, error) {
			return kubernetesmanifest.Result{
				Allowed: false,
				Documents: []kubernetesmanifest.Document{{
					Index:       0,
					Kind:        "Secret",
					Status:      kubernetesmanifest.StatusRefused,
					Requirement: kubernetesresource.ManifestRequirementSecretManage,
					Err:         kubernetesresource.ErrManifestForbidden,
				}},
			}, nil
		},
	}
	response := manifestRequest(t, service, "apply", "?confirm=true", testManifest)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", response.Code, response.Body)
	}
	assertErrorCode(t, response, "forbidden")
}

func TestKubernetesManifestRequestValidation(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesManifestService{
		execute: func(
			context.Context,
			kubernetesmanifest.ResourceAccess,
			kubernetesmanifest.Input,
		) (kubernetesmanifest.Result, error) {
			t.Fatal("Execute() called for an invalid HTTP request")
			return kubernetesmanifest.Result{}, nil
		},
	}
	testCases := []struct {
		name        string
		operation   string
		query       string
		contentType string
		body        []byte
		status      int
		code        string
	}{
		{
			name: "confirmation required", operation: "apply", query: "",
			contentType: "application/yaml", body: []byte(testManifest),
			status: http.StatusBadRequest, code: "confirmation_required",
		},
		{
			name: "invalid boolean", operation: "apply", query: "?dry_run=1",
			contentType: "application/yaml", body: []byte(testManifest),
			status: http.StatusBadRequest, code: "invalid_request",
		},
		{
			name: "unknown query parameter", operation: "apply",
			query:       "?dry_run=true&field_manager=mine",
			contentType: "application/yaml", body: []byte(testManifest),
			status: http.StatusBadRequest, code: "invalid_request",
		},
		{
			// Server-side Apply's conflict switch means nothing to a delete, and
			// an accepted-but-ignored parameter is worse than a refused one.
			name: "force is not a delete option", operation: "delete",
			query:       "?dry_run=true&force=true",
			contentType: "application/yaml", body: []byte(testManifest),
			status: http.StatusBadRequest, code: "invalid_request",
		},
		{
			name: "media type", operation: "apply", query: "?dry_run=true",
			contentType: "application/json", body: []byte("{}"),
			status: http.StatusUnsupportedMediaType, code: "unsupported_media_type",
		},
		{
			name: "too large", operation: "apply", query: "?dry_run=true",
			contentType: "application/yaml",
			body:        bytes.Repeat([]byte("x"), maxKubernetesYAMLBytes+1),
			status:      http.StatusRequestEntityTooLarge, code: "manifest_too_large",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			response := manifestRequestBytes(
				t, service, testCase.operation, testCase.query,
				testCase.contentType, testCase.body,
			)
			if response.Code != testCase.status {
				t.Fatalf(
					"status = %d, want %d: %s",
					response.Code, testCase.status, response.Body,
				)
			}
			assertErrorCode(t, response, testCase.code)
		})
	}
}

func TestKubernetesManifestErrorMapping(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		err    error
		status int
		code   string
	}{
		{kubernetesmanifest.ErrEmptyManifest, http.StatusBadRequest, "empty_manifest"},
		{kubernetesmanifest.ErrTooManyDocuments, http.StatusBadRequest, "too_many_documents"},
		{kubernetesmanifest.ErrInvalidManifest, http.StatusBadRequest, "invalid_yaml"},
		{
			kubernetesresource.ErrAgentNotConnected,
			http.StatusServiceUnavailable,
			"agent_not_connected",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.code, func(t *testing.T) {
			t.Parallel()
			service := &fakeKubernetesManifestService{
				execute: func(
					context.Context,
					kubernetesmanifest.ResourceAccess,
					kubernetesmanifest.Input,
				) (kubernetesmanifest.Result, error) {
					return kubernetesmanifest.Result{}, testCase.err
				},
			}
			response := manifestRequest(
				t, service, "apply", "?dry_run=true", testManifest,
			)
			if response.Code != testCase.status {
				t.Fatalf("status = %d, want %d: %s", response.Code, testCase.status, response.Body)
			}
			assertErrorCode(t, response, testCase.code)
		})
	}
}

// One failure must not have two vocabularies: a rejection an operator has met on
// a single-object save has to arrive here under the same code.
func TestManifestDocumentErrorsReuseTheSharedCodes(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		err  error
		code string
	}{
		{kubernetesresource.ErrManifestForbidden, "forbidden"},
		{kubernetesresource.ErrManifestResourceRefused, "resource_refused"},
		{kubernetesmanifest.ErrUnknownKind, "unknown_kind"},
		{kubernetesmanifest.ErrDuplicateDocument, "duplicate_document"},
		{kubernetesmanifest.ErrDocumentInvalid, "invalid_document"},
		{kubernetesresource.ErrUpstreamRejected, "cluster_api_rejected"},
		{kubernetesresource.ErrIdempotencyConflict, "idempotency_conflict"},
		{kubernetesresource.ErrSecretManagedByPlatform, "secret_managed_by_platform"},
	}
	for _, testCase := range testCases {
		code, message := manifestDocumentError(testCase.err)
		if code != testCase.code {
			t.Errorf("manifestDocumentError(%v) code = %q, want %q", testCase.err, code, testCase.code)
		}
		if message == "" {
			t.Errorf("manifestDocumentError(%v) produced no message", testCase.err)
		}
	}
	if code, message := manifestDocumentError(nil); code != "" || message != "" {
		t.Fatalf("a document with no error reported %q/%q", code, message)
	}
}

// What changed in the Cluster decides how much trail a request leaves: nothing
// changed means one record, objects changed means one record each.
func TestManifestAuditRecordsAggregateWhenNothingChanged(t *testing.T) {
	t.Parallel()

	documents := []kubernetesmanifest.Document{
		{
			Index: 0, APIVersion: "v1", Kind: "ConfigMap",
			Namespace: "team-a", Name: "settings",
			Status: kubernetesmanifest.StatusSucceeded,
		},
		{
			Index: 1, APIVersion: "v1", Kind: "Secret",
			Namespace: "team-a", Name: "credentials",
			Status: kubernetesmanifest.StatusSucceeded,
		},
	}

	// A dry run changed nothing, and it is the request an operator repeats while
	// correcting a file. Sixty rows per preview click would bury the records that
	// name real writes.
	dryRun := manifestAuditRecords(
		kubernetesmanifest.OperationApply,
		true,
		kubernetesmanifest.Result{DryRun: true, Allowed: true, Documents: documents},
	)
	if len(dryRun) != 1 {
		t.Fatalf("a dry run wrote %d records, want 1", len(dryRun))
	}
	if dryRun[0].Action != auditaction.KubernetesResourcePatchDryRun {
		t.Fatalf("dry-run action = %q", dryRun[0].Action)
	}
	if dryRun[0].Result != "succeeded" ||
		dryRun[0].TargetName != "manifest/apply 2 documents" {
		t.Fatalf("dry-run record = %+v", dryRun[0])
	}

	// A refused request changed nothing either, and is repeated until somebody
	// grants the permission. The refused count stays in the target because it is
	// what makes the record a permission boundary rather than a no-op.
	refusedDocuments := []kubernetesmanifest.Document{
		documents[0],
		{
			Index: 1, APIVersion: "v1", Kind: "Secret",
			Namespace: "team-a", Name: "credentials",
			Status: kubernetesmanifest.StatusRefused,
		},
	}
	refused := manifestAuditRecords(
		kubernetesmanifest.OperationApply,
		false,
		kubernetesmanifest.Result{Allowed: false, Documents: refusedDocuments},
	)
	if len(refused) != 1 {
		t.Fatalf("a refused request wrote %d records, want 1", len(refused))
	}
	if refused[0].Result != "failed" ||
		refused[0].TargetName != "manifest/apply 2 documents, 1 refused" {
		t.Fatalf("refused record = %+v", refused[0])
	}
	if refused[0].Action != auditaction.KubernetesResourcePatch {
		t.Fatalf("refused action = %q", refused[0].Action)
	}
}

// An execution changed objects, so the trail has to name each one — including
// the ones that failed or were never reached, which is what tells an operator
// where a stopped run got to.
func TestManifestAuditRecordsNameEveryObjectAnExecutionTouched(t *testing.T) {
	t.Parallel()

	records := manifestAuditRecords(
		kubernetesmanifest.OperationDelete,
		false,
		kubernetesmanifest.Result{
			Allowed: true,
			Failed:  true,
			Documents: []kubernetesmanifest.Document{
				{
					Index: 0, APIVersion: "v1", Kind: "ConfigMap",
					Namespace: "team-a", Name: "first",
					Status: kubernetesmanifest.StatusSucceeded,
				},
				{
					Index: 1, APIVersion: "v1", Kind: "ConfigMap",
					Namespace: "team-a", Name: "second",
					Status: kubernetesmanifest.StatusFailed,
				},
				{
					Index: 2, APIVersion: "v1", Kind: "ConfigMap",
					Namespace: "team-a", Name: "third",
					Status: kubernetesmanifest.StatusNotAttempted,
				},
				{
					// Nothing was asked of the Cluster: the object was already gone.
					Index: 3, APIVersion: "v1", Kind: "ConfigMap",
					Namespace: "team-a", Name: "fourth",
					Status: kubernetesmanifest.StatusSkipped,
				},
			},
		},
	)
	if len(records) != 3 {
		t.Fatalf("wrote %d records, want one per document sent", len(records))
	}
	want := []struct {
		target string
		result string
	}{
		{"v1/ConfigMap team-a/first", "succeeded"},
		{"v1/ConfigMap team-a/second", "failed"},
		{"v1/ConfigMap team-a/third", "failed"},
	}
	for index, expected := range want {
		if records[index].TargetName != expected.target ||
			records[index].Result != expected.result {
			t.Fatalf("record %d = %+v, want %v", index, records[index], expected)
		}
		if records[index].Action != auditaction.KubernetesResourceDelete {
			t.Fatalf("record %d action = %q", index, records[index].Action)
		}
	}
}

type fakeKubernetesManifestService struct {
	execute func(
		context.Context,
		kubernetesmanifest.ResourceAccess,
		kubernetesmanifest.Input,
	) (kubernetesmanifest.Result, error)
}

func (service *fakeKubernetesManifestService) Execute(
	ctx context.Context,
	access kubernetesmanifest.ResourceAccess,
	input kubernetesmanifest.Input,
) (kubernetesmanifest.Result, error) {
	return service.execute(ctx, access, input)
}

func manifestRequest(
	t *testing.T,
	service kubernetesManifestService,
	operation string,
	query string,
	body string,
) *httptest.ResponseRecorder {
	t.Helper()
	return manifestRequestBytes(
		t, service, operation, query, "application/yaml", []byte(body),
	)
}

func manifestRequestBytes(
	t *testing.T,
	service kubernetesManifestService,
	operation string,
	query string,
	contentType string,
	body []byte,
) *httptest.ResponseRecorder {
	t.Helper()
	router := kubernetesManifestHandlerTestRouter(service)
	response := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/clusters/"+testHTTPClusterID+
			"/kubernetes/manifests/"+operation+query,
		bytes.NewReader(body),
	)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set(idempotencyKeyHeaderName, "0123456789abcdef0123")
	router.ServeHTTP(response, request)
	return response
}

func kubernetesManifestHandlerTestRouter(
	service kubernetesManifestService,
) http.Handler {
	configureGinMode.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	router := gin.New()
	handler := newKubernetesManifestHandler(
		discardLogger(),
		service,
		// A non-nil resource service so the handler builds the per-request access
		// rather than reporting the endpoint unavailable. The fake service above
		// never uses it.
		&kubernetesresource.Service{},
		nil,
		5*time.Second,
		KubernetesManifestHTTPConfig{
			RequestTimeout: 30 * time.Second,
			MaxDocuments:   16,
		},
	)
	router.POST(
		"/api/v1/clusters/:cluster_id/kubernetes/manifests/apply",
		handler.apply,
	)
	router.POST(
		"/api/v1/clusters/:cluster_id/kubernetes/manifests/delete",
		handler.delete,
	)
	return router
}
