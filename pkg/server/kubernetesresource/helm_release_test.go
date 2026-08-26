package kubernetesresource

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// helmReleaseSecret builds the object Helm's Secret driver writes: the release
// JSON, gzipped, Base64-encoded by Helm, and then held as raw bytes in a Secret
// whose labels carry the release identity.
func helmReleaseSecret(
	t *testing.T,
	namespace string,
	name string,
	revision int64,
	status string,
	release map[string]any,
	compress bool,
) corev1.Secret {
	t.Helper()
	payload, err := json.Marshal(release)
	if err != nil {
		t.Fatal(err)
	}
	if compress {
		var buffer bytes.Buffer
		writer := gzip.NewWriter(&buffer)
		if _, err := writer.Write(payload); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		payload = buffer.Bytes()
	}
	encoded := base64.StdEncoding.EncodeToString(payload)
	return corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      helmReleaseSecretName(name, revision),
			UID:       "release-uid",
			CreationTimestamp: metav1.NewTime(
				time.Date(2026, time.August, 20, 9, 0, 0, 0, time.UTC),
			),
			Labels: map[string]string{
				helmOwnerLabel:          helmOwnerLabelValue,
				helmReleaseNameLabel:    name,
				helmReleaseVersionLabel: strconv.FormatInt(revision, 10),
				helmReleaseStatusLabel:  status,
			},
		},
		Type: helmReleaseSecretType,
		Data: map[string][]byte{helmReleaseDataKey: []byte(encoded)},
	}
}

func testHelmRelease(name string, revision int64) map[string]any {
	return map[string]any{
		"name":      name,
		"version":   revision,
		"namespace": "analytics",
		"info": map[string]any{
			"first_deployed": "2026-08-01T09:00:00Z",
			"last_deployed":  "2026-08-20T09:00:00Z",
			"description":    "Upgrade complete",
			"status":         "deployed",
			"notes":          "Visit the dashboard.",
		},
		"chart": map[string]any{
			"metadata": map[string]any{
				"name":        "reporting",
				"version":     "2.4.1",
				"appVersion":  "1.9.0",
				"description": "Reporting stack",
			},
			// The chart carries far more than ZKE reads. It is present here so
			// the decoder is exercised against a realistic record rather than a
			// record trimmed to the fields it wants.
			"templates": []any{map[string]any{"name": "templates/deployment.yaml", "data": "Zm9v"}},
		},
		"config":   map[string]any{"replicaCount": float64(3), "auth": map[string]any{"password": "s3cret"}},
		"manifest": "apiVersion: v1\nkind: Service\n",
	}
}

// A listing must reduce the release Secrets to one row per release, at the
// newest revision storage holds — the same reduction `helm list` makes — and
// must ignore Secrets that only look like release Secrets.
func TestListHelmReleasesReducesRevisionsToTheNewestPerRelease(t *testing.T) {
	t.Parallel()

	unrelated := corev1.Secret{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Namespace: "analytics", Name: "database-password"},
		Type:       corev1.SecretTypeOpaque,
	}
	list := corev1.SecretList{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"},
		Items: []corev1.Secret{
			helmReleaseSecret(t, "analytics", "reporting", 1, "superseded", testHelmRelease("reporting", 1), true),
			helmReleaseSecret(t, "analytics", "reporting", 3, "deployed", testHelmRelease("reporting", 3), true),
			helmReleaseSecret(t, "analytics", "reporting", 2, "superseded", testHelmRelease("reporting", 2), true),
			helmReleaseSecret(t, "analytics", "ingest", 1, "failed", testHelmRelease("ingest", 1), true),
			unrelated,
		},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_LIST ||
			request.GetResource().GetResource() != "secrets" ||
			!request.GetSecretAccess() ||
			request.GetNamespace() != "analytics" ||
			request.GetListOptions().GetLabelSelector() != helmReleaseOwnerSelector ||
			request.GetListOptions().GetFieldSelector() != "type="+helmReleaseSecretType {
			t.Fatalf("unexpected Helm release list request: %+v", request)
		}
		return writeKubernetesObject(t, responseBody, &list), nil
	}}

	page, err := NewService(requester).ListHelmReleases(context.Background(), ListHelmReleasesInput{
		ClusterID: testClusterID,
		Namespace: "analytics",
	})
	if err != nil {
		t.Fatalf("ListHelmReleases() err = %v", err)
	}
	if len(page.Releases) != 2 ||
		page.Releases[0].Name != "ingest" || page.Releases[0].Revision != 1 ||
		page.Releases[1].Name != "reporting" || page.Releases[1].Revision != 3 ||
		page.Releases[1].Status != "deployed" ||
		page.Releases[1].SecretName != "sh.helm.release.v1.reporting.v3" {
		t.Fatalf("ListHelmReleases() = %+v", page.Releases)
	}
}

// A Namespace with more revisions than one inventory reads has to refuse rather
// than answer from a partial page: the reduction picks the newest revision it
// saw, and a page boundary would make that the wrong one.
func TestListHelmReleasesRefusesAPartialInventory(t *testing.T) {
	t.Parallel()

	list := corev1.SecretList{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"},
		ListMeta: metav1.ListMeta{Continue: "next-page"},
		Items: []corev1.Secret{
			helmReleaseSecret(t, "analytics", "reporting", 1, "deployed", testHelmRelease("reporting", 1), true),
		},
	}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		_ *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return writeKubernetesObject(t, responseBody, &list), nil
	}}

	_, err := NewService(requester).ListHelmReleases(context.Background(), ListHelmReleasesInput{
		ClusterID: testClusterID,
		Namespace: "analytics",
	})
	if !errors.Is(err, ErrHelmReleaseInventoryTruncated) {
		t.Fatalf("ListHelmReleases() err = %v, want ErrHelmReleaseInventoryTruncated", err)
	}
}

// Reading one release unwraps all three layers Helm's driver writes and reports
// what only the payload knows — chart, values, notes. Values are returned in
// full, which is exactly why these routes require `cluster.secret.read`.
func TestGetHelmReleaseDecodesTheStoredRecord(t *testing.T) {
	t.Parallel()

	for _, compressed := range []bool{true, false} {
		secret := helmReleaseSecret(t, "analytics", "reporting", 3, "deployed", testHelmRelease("reporting", 3), compressed)
		requester := &fakeResourceRequester{handle: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			responseBody io.Writer,
		) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET ||
				request.GetName() != "sh.helm.release.v1.reporting.v3" ||
				!request.GetSecretAccess() {
				t.Fatalf("unexpected Helm release read: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, &secret), nil
		}}

		detail, err := NewService(requester).GetHelmRelease(
			context.Background(), testClusterID, "analytics", "reporting", 3,
		)
		if err != nil {
			t.Fatalf("compressed=%v GetHelmRelease() err = %v", compressed, err)
		}
		if detail.Name != "reporting" || detail.Revision != 3 ||
			detail.ChartName != "reporting" || detail.ChartVersion != "2.4.1" ||
			detail.AppVersion != "1.9.0" || detail.Status != "deployed" ||
			detail.Description != "Upgrade complete" ||
			detail.Notes != "Visit the dashboard." ||
			detail.Manifest != "apiVersion: v1\nkind: Service\n" ||
			detail.ManifestTruncated ||
			detail.LastDeployed == nil || detail.FirstDeployed == nil {
			t.Fatalf("compressed=%v GetHelmRelease() = %+v", compressed, detail)
		}
		auth, ok := detail.Values["auth"].(map[string]any)
		if !ok || auth["password"] != "s3cret" {
			t.Fatalf("compressed=%v release values = %+v", compressed, detail.Values)
		}
	}
}

// A Secret that carries the release type but is not a release ZKE can read must
// come back as an unreadable release, not as a cluster failure and not as an
// empty release that looks installed.
func TestGetHelmReleaseReportsAnUndecodablePayload(t *testing.T) {
	t.Parallel()

	secret := helmReleaseSecret(t, "analytics", "reporting", 1, "deployed", testHelmRelease("reporting", 1), false)
	secret.Data[helmReleaseDataKey] = []byte("not-base64-at-all!!")
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		_ *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		return writeKubernetesObject(t, responseBody, &secret), nil
	}}

	_, err := NewService(requester).GetHelmRelease(
		context.Background(), testClusterID, "analytics", "reporting", 1,
	)
	if !errors.Is(err, ErrHelmReleaseUnreadable) {
		t.Fatalf("GetHelmRelease() err = %v, want ErrHelmReleaseUnreadable", err)
	}
}

// gzip over repetitive JSON expands enormously, so a release Secret well inside
// Kubernetes' own 1 MiB limit can still decompress to something no request
// should allocate. The ceiling is enforced rather than trusted.
func TestDecodeHelmReleaseRefusesAnOversizedDecompressedPayload(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	writer := gzip.NewWriter(&buffer)
	if _, err := io.CopyN(writer, repeatingReader{}, int64(maximumHelmReleaseBytes)+1); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	stored := base64.StdEncoding.EncodeToString([]byte(
		base64.StdEncoding.EncodeToString(buffer.Bytes()),
	))
	if _, err := decodeHelmRelease(stored); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("decodeHelmRelease() err = %v, want ErrResponseTooLarge", err)
	}
}

type repeatingReader struct{}

func (repeatingReader) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = ' '
	}
	return len(buffer), nil
}

// A release name that never existed has to be a missing release rather than a
// missing Secret: the caller asked about a release, and the Secret name is the
// storage driver's business.
func TestHelmReleaseRevisionsReportAMissingReleaseAsSuch(t *testing.T) {
	t.Parallel()

	empty := corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}}
	requester := &fakeResourceRequester{handle: func(
		_ context.Context,
		_ string,
		request *agentv1.ResourceRequest,
		responseBody io.Writer,
	) (*agentv1.ResourceResponse, error) {
		if !strings.Contains(request.GetListOptions().GetLabelSelector(), helmReleaseNameLabel+"=reporting") {
			t.Fatalf("history did not select one release: %+v", request.GetListOptions())
		}
		return writeKubernetesObject(t, responseBody, &empty), nil
	}}

	_, err := NewService(requester).ListHelmReleaseRevisions(
		context.Background(), testClusterID, "analytics", "reporting",
	)
	if !errors.Is(err, ErrHelmReleaseNotFound) {
		t.Fatalf("ListHelmReleaseRevisions() err = %v, want ErrHelmReleaseNotFound", err)
	}
}
