package httpapi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auditaction"
	"github.com/togettoyou/zke/pkg/server/auth"
	httpmiddleware "github.com/togettoyou/zke/pkg/server/httpapi/middleware"
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

type fakeKubernetesHelmReleaseService struct {
	listInput      kubernetesresource.ListHelmReleasesInput
	revisionsName  string
	getName        string
	getRevision    int64
	getErr         error
	revisionsCalls int
}

func (service *fakeKubernetesHelmReleaseService) ListHelmReleases(
	_ context.Context,
	input kubernetesresource.ListHelmReleasesInput,
) (kubernetesresource.HelmReleasePage, error) {
	service.listInput = input
	return kubernetesresource.HelmReleasePage{
		Releases: []kubernetesresource.HelmRelease{{
			Namespace:  input.Namespace,
			Name:       "reporting",
			Revision:   3,
			Status:     "deployed",
			SecretName: "sh.helm.release.v1.reporting.v3",
		}},
	}, nil
}

func (service *fakeKubernetesHelmReleaseService) ListHelmReleaseRevisions(
	_ context.Context,
	_ string,
	namespace string,
	name string,
) (kubernetesresource.HelmReleasePage, error) {
	service.revisionsCalls++
	service.revisionsName = name
	return kubernetesresource.HelmReleasePage{
		Releases: []kubernetesresource.HelmRelease{{
			Namespace: namespace,
			Name:      name,
			Revision:  3,
			Status:    "deployed",
		}},
	}, nil
}

func (service *fakeKubernetesHelmReleaseService) GetHelmRelease(
	_ context.Context,
	_ string,
	namespace string,
	name string,
	revision int64,
) (kubernetesresource.HelmReleaseDetail, error) {
	service.getName = name
	service.getRevision = revision
	if service.getErr != nil {
		return kubernetesresource.HelmReleaseDetail{}, service.getErr
	}
	return kubernetesresource.HelmReleaseDetail{
		HelmRelease: kubernetesresource.HelmRelease{
			Namespace: namespace,
			Name:      name,
			Revision:  3,
			Status:    "deployed",
		},
		ChartName:    "reporting",
		ChartVersion: "2.4.1",
		Values:       map[string]any{"replicaCount": 3},
	}, nil
}

// Reading a Helm release is reading a Secret, so it leaves the same kind of
// record a Secret read leaves — and the listing, which returns no values at all,
// is recorded apart from the read that does.
func TestKubernetesHelmReleaseHandlersAuditReadsLikeSecretReads(t *testing.T) {
	t.Parallel()

	const clusterID = "00000000-0000-4000-8000-000000000003"
	service := &fakeKubernetesHelmReleaseService{}
	auditStore := &recordingPodAuditStore{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler := newKubernetesHelmReleaseHandler(
		logger,
		service,
		audit.NewService(auditStore, nil),
		time.Second,
	)
	router := gin.New()
	router.Use(httpmiddleware.RequestLogger(logger))
	router.Use(func(c *gin.Context) {
		c.Set("authenticated_identity", auth.Identity{
			User: auth.User{ID: "00000000-0000-4000-8000-000000000001"},
		})
		c.Next()
	})
	base := "/clusters/:cluster_id/namespaces/:namespace_name/helm-releases"
	router.GET(base, handler.list)
	router.GET(base+"/:release_name", handler.get)
	router.GET(base+"/:release_name/revisions", handler.revisions)
	baseURL := "/clusters/" + clusterID + "/namespaces/analytics/helm-releases"

	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, baseURL, nil))
	if response.Code != http.StatusOK ||
		service.listInput.ClusterID != clusterID ||
		service.listInput.Namespace != "analytics" ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"releases"`)) {
		t.Fatalf("list status=%d input=%+v body=%s", response.Code, service.listInput, response.Body.String())
	}
	if len(auditStore.events) != 1 ||
		auditStore.events[0].Action != auditaction.KubernetesHelmReleaseList ||
		auditStore.events[0].Result != "succeeded" {
		t.Fatalf("unexpected Helm release list audit events: %+v", auditStore.events)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, baseURL+"/reporting?revision=2", nil))
	if response.Code != http.StatusOK ||
		service.getName != "reporting" ||
		service.getRevision != 2 ||
		!bytes.Contains(response.Body.Bytes(), []byte(`"chart_version":"2.4.1"`)) {
		t.Fatalf("get status=%d revision=%d body=%s", response.Code, service.getRevision, response.Body.String())
	}
	if len(auditStore.events) != 2 ||
		auditStore.events[1].Action != auditaction.KubernetesHelmReleaseRead {
		t.Fatalf("unexpected Helm release read audit event: %+v", auditStore.events)
	}

	// No revision means whichever one storage holds as newest, which the service
	// signals with 0 rather than by guessing a number here.
	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, baseURL+"/reporting", nil))
	if response.Code != http.StatusOK || service.getRevision != 0 {
		t.Fatalf("latest revision status=%d revision=%d", response.Code, service.getRevision)
	}

	response = httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, baseURL+"/reporting/revisions", nil))
	if response.Code != http.StatusOK ||
		service.revisionsName != "reporting" ||
		service.revisionsCalls != 1 {
		t.Fatalf("revisions status=%d name=%q body=%s", response.Code, service.revisionsName, response.Body.String())
	}
	// The history returns no values, so it is recorded as a listing rather than
	// as a read.
	if len(auditStore.events) != 4 ||
		auditStore.events[3].Action != auditaction.KubernetesHelmReleaseList {
		t.Fatalf("unexpected Helm release history audit event: %+v", auditStore.events)
	}
}

// The refusals these endpoints make on their own have to stay distinguishable: a
// release that does not exist is a 404 about a release, and a payload this
// Server cannot decode is the Cluster's answer being unusable rather than this
// Server failing.
func TestKubernetesHelmReleaseHandlersSeparateMissingFromUndecodable(t *testing.T) {
	t.Parallel()

	for name, testCase := range map[string]struct {
		err    error
		status int
		code   string
	}{
		"missing": {
			err:    kubernetesresource.ErrHelmReleaseNotFound,
			status: http.StatusNotFound,
			code:   "helm_release_not_found",
		},
		"undecodable": {
			err:    kubernetesresource.ErrHelmReleaseUnreadable,
			status: http.StatusBadGateway,
			code:   "helm_release_unreadable",
		},
		"truncated inventory": {
			err:    kubernetesresource.ErrHelmReleaseInventoryTruncated,
			status: http.StatusUnprocessableEntity,
			code:   "helm_release_inventory_truncated",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			service := &fakeKubernetesHelmReleaseService{getErr: testCase.err}
			handler := newKubernetesHelmReleaseHandler(
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				service,
				nil,
				time.Second,
			)
			router := gin.New()
			router.GET(
				"/clusters/:cluster_id/namespaces/:namespace_name/helm-releases/:release_name",
				handler.get,
			)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(
				http.MethodGet,
				"/clusters/00000000-0000-4000-8000-000000000003"+
					"/namespaces/analytics/helm-releases/reporting",
				nil,
			))
			if response.Code != testCase.status ||
				!bytes.Contains(response.Body.Bytes(), []byte(testCase.code)) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

// A revision is a positive integer or absent. Anything else names no revision at
// all and must not reach the Cluster as a guess.
func TestKubernetesHelmReleaseHandlerRejectsUnusableRevisions(t *testing.T) {
	t.Parallel()

	service := &fakeKubernetesHelmReleaseService{}
	handler := newKubernetesHelmReleaseHandler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		service,
		nil,
		time.Second,
	)
	router := gin.New()
	base := "/clusters/:cluster_id/namespaces/:namespace_name/helm-releases"
	router.GET(base, handler.list)
	router.GET(base+"/:release_name", handler.get)
	baseURL := "/clusters/00000000-0000-4000-8000-000000000003/namespaces/analytics/helm-releases"

	for _, path := range []string{
		baseURL + "/reporting?revision=0",
		baseURL + "/reporting?revision=-1",
		baseURL + "/reporting?revision=latest",
		baseURL + "/reporting?history=true",
		baseURL + "?limit=10",
	} {
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusBadRequest {
			t.Errorf("GET %s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if service.getName != "" || service.listInput.ClusterID != "" {
		t.Fatal("invalid Helm release request reached the service")
	}
}
