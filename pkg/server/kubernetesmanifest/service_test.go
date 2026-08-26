package kubernetesmanifest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

const testClusterID = "0f6f2c2a-6b1a-4a0c-9d3a-4a1f2b3c4d5e"

func TestApplyPlansCreateAndUpdateFromTheCluster(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.existing["v1/deployments/team-a/api"] = liveObject("uid-api", "42")
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})

	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest: []byte(document("apps/v1", "Deployment", "team-a", "api") +
			"---\n" + document("v1", "ConfigMap", "team-a", "settings")),
		Operation: OperationApply,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !result.Allowed || result.Failed {
		t.Fatalf("result allowed=%v failed=%v", result.Allowed, result.Failed)
	}
	if result.Documents[0].Action != ActionUpdate {
		t.Fatalf("existing object planned as %q, want update", result.Documents[0].Action)
	}
	if result.Documents[0].UID != "uid-api" {
		t.Fatalf("update plan did not carry the live UID: %q", result.Documents[0].UID)
	}
	if result.Documents[1].Action != ActionCreate {
		t.Fatalf("absent object planned as %q, want create", result.Documents[1].Action)
	}
	// A dry run that reached the API Server and was accepted is a document that is
	// ready, not one that happened. Reporting it as succeeded read as if the object
	// had been written — the one thing a dry run must never appear to have done.
	for _, document := range result.Documents {
		if document.Status != StatusPlanned || !document.Previewed {
			t.Fatalf(
				"document %d status = %q previewed = %v; want planned and previewed",
				document.Index, document.Status, document.Previewed,
			)
		}
	}
	if len(access.applied) != 2 {
		t.Fatalf("applied %d documents, want 2", len(access.applied))
	}
	// A dry run must reach the Cluster as a dry run. Planning that skipped
	// Kubernetes' own validation would report a manifest as ready that the API
	// Server is about to reject.
	for _, applied := range access.applied {
		if !applied.DryRun {
			t.Fatal("a dry-run plan sent a write that was not a dry run")
		}
	}
}

// The whole point of resolving permissions per document: a caller who may create
// generic resources still may not write a Secret, and a manifest holding one is
// refused whole rather than half-applied.
func TestApplyRefusesTheWholeManifestWhenOneDocumentIsNotCovered(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.grant = kubernetesresource.ManifestGrant{
		ResourceCreate: true,
		ResourceUpdate: true,
	}
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})

	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest: []byte(document("v1", "ConfigMap", "team-a", "settings") +
			"---\n" + document("v1", "Secret", "team-a", "credentials")),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Allowed {
		t.Fatal("a manifest holding a refused document was reported as allowed")
	}
	if result.Documents[1].Status != StatusRefused {
		t.Fatalf("Secret document status = %q, want refused", result.Documents[1].Status)
	}
	if result.Documents[1].Requirement != kubernetesresource.ManifestRequirementSecretManage {
		t.Fatalf("Secret refusal named %q", result.Documents[1].Requirement)
	}
	// The ConfigMap the caller *was* allowed to write must not have been written.
	// A partial apply chosen by permissions rather than by the operator is the
	// half nobody asked for on its own.
	if len(access.applied) != 0 {
		t.Fatalf("a refused manifest wrote %d documents", len(access.applied))
	}
}

// A manifest that creates a Namespace and then fills it is the most ordinary
// multi-document manifest there is, and server-side dry run cannot check any of
// it: the dry-run creation of the Namespace persists nothing, so every later
// document lands in a Namespace that does not exist. Reported as failures, they
// also stopped the run at the second document — which made such a manifest
// impossible to apply through ZKE at all.
func TestDryRunDoesNotFailDocumentsInsideANamespaceTheManifestCreates(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	// What Kubernetes answers when a Namespace does not exist yet.
	access.applyErr = map[string]error{
		"settings": kubernetesresource.ErrResourceNotFound,
	}
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})

	manifest := document("v1", "Namespace", "", "test-env") +
		"---\n" + document("v1", "ConfigMap", "test-env", "settings")
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationApply,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Failed {
		t.Fatal("a manifest creating its own Namespace was reported as a failed dry run")
	}
	// The Namespace itself is validated normally: it needs nothing that does not
	// exist yet.
	if result.Documents[0].Status != StatusPlanned || !result.Documents[0].Previewed {
		t.Fatalf("Namespace document = %+v", result.Documents[0])
	}
	// Its contents are planned but honestly marked as unchecked, rather than
	// either failed or silently presented as validated.
	if result.Documents[1].Status != StatusPlanned {
		t.Fatalf("ConfigMap status = %q, want planned", result.Documents[1].Status)
	}
	if result.Documents[1].Previewed {
		t.Fatal("a document that was never sent to the Cluster was reported as previewed")
	}
	for _, applied := range access.applied {
		if applied.Name == "settings" {
			t.Fatal("a document was sent into a Namespace the dry run had not created")
		}
	}
}

// The skip is only for Namespaces this manifest is about to create. One that
// already exists validates normally, and losing that check would be losing most
// of the value of the preview.
func TestDryRunStillValidatesDocumentsInAnExistingNamespace(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.existing["v1/namespaces//test-env"] = liveObject("uid-ns", "7")
	access.applyErr = map[string]error{
		"settings": kubernetesresource.ErrUpstreamRejected,
	}
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})

	manifest := document("v1", "Namespace", "", "test-env") +
		"---\n" + document("v1", "ConfigMap", "test-env", "settings")
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationApply,
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !result.Failed || result.Documents[1].Status != StatusFailed {
		t.Fatalf("a rejected document in an existing Namespace was not reported: %+v", result.Documents[1])
	}
}

// Actually applying the manifest sends everything: the Namespace is created for
// real by the document before it.
func TestExecutionSendsDocumentsInsideANamespaceTheManifestCreates(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	manifest := document("v1", "Namespace", "", "test-env") +
		"---\n" + document("v1", "ConfigMap", "test-env", "settings")
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if len(access.applied) != 2 {
		t.Fatalf("applied %d documents, want both", len(access.applied))
	}
	for _, item := range result.Documents {
		if item.Status != StatusSucceeded {
			t.Fatalf("document %d status = %q, want succeeded", item.Index, item.Status)
		}
	}
}

func TestApplyResolvesTheNamespace(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	testCases := []struct {
		name             string
		documentNamekind string
		requestNamespace string
		wantStatus       Status
		wantNamespace    string
	}{
		{
			name:             "request namespace fills in a document without one",
			documentNamekind: document("v1", "ConfigMap", "", "settings"),
			requestNamespace: "team-a",
			wantStatus:       StatusSucceeded,
			wantNamespace:    "team-a",
		},
		{
			name:             "a document keeps its own namespace when they agree",
			documentNamekind: document("v1", "ConfigMap", "team-a", "settings"),
			requestNamespace: "team-a",
			wantStatus:       StatusSucceeded,
			wantNamespace:    "team-a",
		},
		{
			// Silently moving an object to another Namespace is the one
			// interpretation nobody asks for.
			name:             "a contradicting namespace is refused, not overridden",
			documentNamekind: document("v1", "ConfigMap", "team-b", "settings"),
			requestNamespace: "team-a",
			wantStatus:       StatusInvalid,
		},
		{
			name:             "a namespaced document with no namespace anywhere is invalid",
			documentNamekind: document("v1", "ConfigMap", "", "settings"),
			requestNamespace: "",
			wantStatus:       StatusInvalid,
		},
		{
			name:             "a namespace on a cluster-scoped object is invalid",
			documentNamekind: document("v1", "Namespace", "team-a", "team-b"),
			requestNamespace: "",
			wantStatus:       StatusInvalid,
		},
		{
			name:             "a cluster-scoped object does not take the request namespace",
			documentNamekind: document("v1", "Namespace", "", "team-b"),
			requestNamespace: "team-a",
			wantStatus:       StatusSucceeded,
			wantNamespace:    "",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			access := newFakeAccess()
			result, err := service.Execute(context.Background(), access, Input{
				ClusterID: testClusterID,
				Manifest:  []byte(testCase.documentNamekind),
				Namespace: testCase.requestNamespace,
				Operation: OperationApply,
				Confirm:   true,
			})
			if err != nil {
				t.Fatalf("Execute() = %v", err)
			}
			document := result.Documents[0]
			if document.Status != testCase.wantStatus {
				t.Fatalf(
					"status = %q (%v), want %q",
					document.Status, document.Err, testCase.wantStatus,
				)
			}
			if testCase.wantStatus != StatusSucceeded {
				return
			}
			if document.Namespace != testCase.wantNamespace {
				t.Fatalf("namespace = %q, want %q", document.Namespace, testCase.wantNamespace)
			}
			// The resolved Namespace has to be in the object that was sent, not
			// only in the plan beside it: the resource layer refuses a body whose
			// own `metadata.namespace` disagrees with its request's scope, so a
			// Namespace supplied once for the whole file would otherwise come back
			// as an invalid-request error on every document in it.
			sent := objectNamespace(access.applied[0].Object)
			if sent != testCase.wantNamespace {
				t.Fatalf("applied object namespace = %q, want %q", sent, testCase.wantNamespace)
			}
		})
	}
}

func TestApplyRefusesDocumentsItCannotTurnIntoRequests(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	testCases := []struct {
		name     string
		manifest string
		wantErr  error
	}{
		{
			name:     "a Kind the Cluster does not serve",
			manifest: document("example.com/v1", "Widget", "team-a", "one"),
			wantErr:  ErrUnknownKind,
		},
		{
			name:     "no name",
			manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  namespace: team-a\n",
			wantErr:  ErrDocumentInvalid,
		},
		{
			name: "generateName instead of a name",
			manifest: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n" +
				"  namespace: team-a\n  generateName: settings-\n",
			wantErr: ErrDocumentInvalid,
		},
		{
			name:     "no kind",
			manifest: "apiVersion: v1\nmetadata:\n  name: settings\n",
			wantErr:  ErrDocumentInvalid,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			result, err := service.Execute(context.Background(), newFakeAccess(), Input{
				ClusterID: testClusterID,
				Manifest:  []byte(testCase.manifest),
				Operation: OperationApply,
				Confirm:   true,
			})
			if err != nil {
				t.Fatalf("Execute() = %v", err)
			}
			document := result.Documents[0]
			if document.Status != StatusInvalid {
				t.Fatalf("status = %q, want invalid", document.Status)
			}
			if !errors.Is(document.Err, testCase.wantErr) {
				t.Fatalf("document error = %v, want %v", document.Err, testCase.wantErr)
			}
		})
	}
}

// Two documents naming one object make the result depend on which came last,
// and for a delete they turn a success into a reported failure.
func TestManifestRefusesTwoDocumentsNamingTheSameObject(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	manifest := document("v1", "ConfigMap", "team-a", "settings") +
		"---\n" + document("v1", "ConfigMap", "team-a", "settings")
	result, err := service.Execute(context.Background(), newFakeAccess(), Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !errors.Is(result.Documents[1].Err, ErrDuplicateDocument) {
		t.Fatalf("second document error = %v, want ErrDuplicateDocument", result.Documents[1].Err)
	}
}

// A manifest is written container-first. Deleting in that order would take the
// dependents with the container before the file ever names them.
func TestDeleteRunsBackwardsThroughTheManifest(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.existing["v1/namespaces//team-a"] = liveObject("uid-ns", "1")
	access.existing["v1/configmaps/team-a/settings"] = liveObject("uid-cm", "2")
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})

	manifest := document("v1", "Namespace", "", "team-a") +
		"---\n" + document("v1", "ConfigMap", "team-a", "settings")
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationDelete,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if len(access.deleted) != 2 {
		t.Fatalf("deleted %d objects, want 2", len(access.deleted))
	}
	if access.deleted[0].Name != "settings" || access.deleted[1].Name != "team-a" {
		t.Fatalf(
			"deleted in order %q, %q; want the ConfigMap before its Namespace",
			access.deleted[0].Name, access.deleted[1].Name,
		)
	}
	// The precondition is what stops a delete landing on a same-named object
	// created since the manifest was read.
	if access.deleted[0].Current == nil {
		t.Fatal("delete was sent without the live object it was planned against")
	}
	// Reported in submitted order whatever order they ran in, or the result does
	// not line up with the file the operator is reading.
	if result.Documents[0].Kind != "Namespace" {
		t.Fatalf("results were reordered: %q first", result.Documents[0].Kind)
	}
}

func TestDeleteSkipsObjectsThatAreAlreadyGone(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(document("v1", "ConfigMap", "team-a", "settings")),
		Operation: OperationDelete,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if result.Failed {
		t.Fatal("deleting an absent object was reported as a failure")
	}
	if result.Documents[0].Status != StatusSkipped ||
		result.Documents[0].Action != ActionAbsent {
		t.Fatalf(
			"absent object reported as %q/%q",
			result.Documents[0].Status, result.Documents[0].Action,
		)
	}
	if len(access.deleted) != 0 {
		t.Fatal("a delete was sent for an object that does not exist")
	}
}

// Kubernetes has no transaction, so the honest thing is to stop and say exactly
// which documents were written, which failed, and which were never tried.
func TestApplyStopsAtTheFirstFailureAndNamesWhatWasNotAttempted(t *testing.T) {
	t.Parallel()

	access := newFakeAccess()
	access.applyErr = map[string]error{
		"second": kubernetesresource.ErrUpstreamRejected,
	}
	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})

	manifest := document("v1", "ConfigMap", "team-a", "first") +
		"---\n" + document("v1", "ConfigMap", "team-a", "second") +
		"---\n" + document("v1", "ConfigMap", "team-a", "third")
	result, err := service.Execute(context.Background(), access, Input{
		ClusterID: testClusterID,
		Manifest:  []byte(manifest),
		Operation: OperationApply,
		Confirm:   true,
	})
	if err != nil {
		t.Fatalf("Execute() = %v", err)
	}
	if !result.Failed {
		t.Fatal("a manifest with a rejected document was not reported as failed")
	}
	wantStatus := []Status{StatusSucceeded, StatusFailed, StatusNotAttempted}
	for index, want := range wantStatus {
		if result.Documents[index].Status != want {
			t.Fatalf(
				"document %d status = %q, want %q",
				index, result.Documents[index].Status, want,
			)
		}
	}
	if len(access.applied) != 2 {
		t.Fatalf("sent %d writes, want it to stop after the failure", len(access.applied))
	}
}

// The Agent's replay cache keys on the request. Sending thirty different objects
// under one key would make the second document look like a repeat of the first.
func TestEachDocumentGetsItsOwnStableIdempotencyKey(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	manifest := document("v1", "ConfigMap", "team-a", "first") +
		"---\n" + document("v1", "ConfigMap", "team-a", "second")
	run := func() []string {
		access := newFakeAccess()
		if _, err := service.Execute(context.Background(), access, Input{
			ClusterID:      testClusterID,
			Manifest:       []byte(manifest),
			Operation:      OperationApply,
			Confirm:        true,
			IdempotencyKey: "0123456789abcdef0123",
		}); err != nil {
			t.Fatalf("Execute() = %v", err)
		}
		keys := make([]string, 0, len(access.applied))
		for _, applied := range access.applied {
			keys = append(keys, applied.IdempotencyKey)
		}
		return keys
	}

	first := run()
	if first[0] == first[1] {
		t.Fatal("two documents were sent under the same idempotency key")
	}
	for _, key := range first {
		// The resource layer refuses anything outside 16..128 bytes.
		if len(key) < 16 || len(key) > 128 || strings.TrimSpace(key) != key {
			t.Fatalf("derived key %q is not a valid idempotency key", key)
		}
	}
	// Stable across retries of the same request, or a retry after a timeout
	// would execute a second time instead of replaying.
	second := run()
	for index := range first {
		if first[index] != second[index] {
			t.Fatalf("key for document %d changed between identical requests", index)
		}
	}
}

// Reordering a file must not hand one object the key another was written under.
func TestIdempotencyKeysFollowTheObjectNotThePosition(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	keyFor := func(manifest string, name string) string {
		access := newFakeAccess()
		if _, err := service.Execute(context.Background(), access, Input{
			ClusterID:      testClusterID,
			Manifest:       []byte(manifest),
			Operation:      OperationApply,
			Confirm:        true,
			IdempotencyKey: "0123456789abcdef0123",
		}); err != nil {
			t.Fatalf("Execute() = %v", err)
		}
		for _, applied := range access.applied {
			if applied.Name == name {
				return applied.IdempotencyKey
			}
		}
		t.Fatalf("no write for %q", name)
		return ""
	}

	forward := document("v1", "ConfigMap", "team-a", "first") +
		"---\n" + document("v1", "ConfigMap", "team-a", "second")
	reversed := document("v1", "ConfigMap", "team-a", "second") +
		"---\n" + document("v1", "ConfigMap", "team-a", "first")
	if keyFor(forward, "first") != keyFor(reversed, "first") {
		t.Fatal("an object's idempotency key changed when the file was reordered")
	}
}

// A dry run and the execution that follows it are different requests against the
// same object, so they must not share a key or the execution would replay the
// dry run's cached result.
func TestDryRunAndExecutionUseDifferentIdempotencyKeys(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 10, FieldManager: "zke-manifest"})
	keyFor := func(dryRun bool) string {
		access := newFakeAccess()
		if _, err := service.Execute(context.Background(), access, Input{
			ClusterID:      testClusterID,
			Manifest:       []byte(document("v1", "ConfigMap", "team-a", "settings")),
			Operation:      OperationApply,
			DryRun:         dryRun,
			Confirm:        !dryRun,
			IdempotencyKey: "0123456789abcdef0123",
		}); err != nil {
			t.Fatalf("Execute() = %v", err)
		}
		return access.applied[0].IdempotencyKey
	}
	if keyFor(true) == keyFor(false) {
		t.Fatal("the dry run and the execution shared an idempotency key")
	}
}

func TestExecuteRejectsAnUnusableManifest(t *testing.T) {
	t.Parallel()

	service := NewService(Config{MaxDocuments: 2, FieldManager: "zke-manifest"})
	testCases := []struct {
		name     string
		manifest string
		wantErr  error
	}{
		{name: "empty", manifest: "\n", wantErr: ErrEmptyManifest},
		{
			name: "too many documents",
			manifest: document("v1", "ConfigMap", "team-a", "a") + "---\n" +
				document("v1", "ConfigMap", "team-a", "b") + "---\n" +
				document("v1", "ConfigMap", "team-a", "c"),
			wantErr: ErrTooManyDocuments,
		},
		{name: "unparseable", manifest: "kind: a\nkind: b\n", wantErr: ErrInvalidManifest},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := service.Execute(context.Background(), newFakeAccess(), Input{
				ClusterID: testClusterID,
				Manifest:  []byte(testCase.manifest),
				Operation: OperationApply,
				Confirm:   true,
			})
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("Execute() = %v, want %v", err, testCase.wantErr)
			}
		})
	}
}

func document(apiVersion string, kind string, namespace string, name string) string {
	builder := &strings.Builder{}
	builder.WriteString("apiVersion: " + apiVersion + "\n")
	builder.WriteString("kind: " + kind + "\n")
	builder.WriteString("metadata:\n")
	builder.WriteString("  name: " + name + "\n")
	if namespace != "" {
		builder.WriteString("  namespace: " + namespace + "\n")
	}
	return builder.String()
}

func liveObject(uid string, resourceVersion string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"uid":             uid,
			"resourceVersion": resourceVersion,
		},
	}
}

// fakeAccess stands in for the resource layer so the planning, ordering and
// permission rules can be tested without a Cluster. It answers the same
// requirement questions kubernetesresource.ManifestAccess does; the mapping from
// family to permission is tested against the real one in that package.
type fakeAccess struct {
	grant    kubernetesresource.ManifestGrant
	existing map[string]map[string]any
	applyErr map[string]error
	applied  []kubernetesresource.ManifestApplyInput
	deleted  []kubernetesresource.ManifestDeleteInput
}

func newFakeAccess() *fakeAccess {
	return &fakeAccess{
		grant: kubernetesresource.ManifestGrant{
			ResourceCreate:  true,
			ResourceUpdate:  true,
			ResourceDelete:  true,
			NamespaceManage: true,
			SecretRead:      true,
			SecretManage:    true,
			RBACManage:      true,
		},
		existing: map[string]map[string]any{},
		applyErr: map[string]error{},
	}
}

var fakeCatalog = map[kubernetesresource.ManifestKind]kubernetesresource.ManifestCatalogEntry{
	{Version: "v1", Kind: "ConfigMap"}: {
		Resource:   kubernetesresource.ResourceIdentity{Version: "v1", Resource: "configmaps"},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Version: "v1", Kind: "Secret"}: {
		Resource:   kubernetesresource.ResourceIdentity{Version: "v1", Resource: "secrets"},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Version: "v1", Kind: "Namespace"}: {
		Resource:   kubernetesresource.ResourceIdentity{Version: "v1", Resource: "namespaces"},
		Namespaced: false,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Group: "apps", Version: "v1", Kind: "Deployment"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Group: "apps", Version: "v1", Resource: "deployments",
		},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Version: "v1", Kind: "Service"}: {
		Resource:   kubernetesresource.ResourceIdentity{Version: "v1", Resource: "services"},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Version: "v1", Kind: "PersistentVolumeClaim"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Version: "v1", Resource: "persistentvolumeclaims",
		},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Group: "networking.k8s.io", Version: "v1", Kind: "Ingress"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Group: "networking.k8s.io", Version: "v1", Resource: "ingresses",
		},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	// The five authorization families. They are in the catalog because a real
	// Cluster reports them — only the Server's own resource browser filters them
	// out — and a fixture missing them would make a manifest holding a Role look
	// like an unknown Kind rather than a document answering to
	// `cluster.rbac.manage`, which is the distinction most of these tests are about.
	{Version: "v1", Kind: "ServiceAccount"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Version: "v1", Resource: "serviceaccounts",
		},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles",
		},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "RoleBinding"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings",
		},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRole"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles",
		},
		Namespaced: false,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "ClusterRoleBinding"}: {
		Resource: kubernetesresource.ResourceIdentity{
			Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings",
		},
		Namespaced: false,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	// Cluster-scoped and in the catalog because a manifest may legitimately name
	// one: a Node document answers to `cluster.node.manage` rather than to the
	// ordinary resource permissions, and that is only testable against a Kind
	// that resolves.
	{Version: "v1", Kind: "Node"}: {
		Resource:   kubernetesresource.ResourceIdentity{Version: "v1", Resource: "nodes"},
		Namespaced: false,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
	// Refused by the resource layer whatever the grant, and in the catalog so the
	// refusal is tested against a Kind that resolves rather than one that does not.
	{Version: "v1", Kind: "Event"}: {
		Resource:   kubernetesresource.ResourceIdentity{Version: "v1", Resource: "events"},
		Namespaced: true,
		Verbs:      []string{"get", "list", "create", "update", "patch", "delete"},
	},
}

func (access *fakeAccess) DiscoverResources(
	context.Context,
	string,
) (kubernetesresource.ManifestCatalog, error) {
	return kubernetesresource.ManifestCatalog{Entries: fakeCatalog}, nil
}

func (access *fakeAccess) key(
	resource kubernetesresource.ResourceIdentity,
	namespace string,
	name string,
) string {
	return resource.Version + "/" + resource.Resource + "/" + namespace + "/" + name
}

func (access *fakeAccess) GetResource(
	_ context.Context,
	input kubernetesresource.ManifestGetInput,
) (map[string]any, error) {
	object, exists := access.existing[access.key(
		input.Resource, input.Namespace, input.Name,
	)]
	if !exists {
		return nil, kubernetesresource.ErrResourceNotFound
	}
	return object, nil
}

func (access *fakeAccess) ApplyResource(
	_ context.Context,
	input kubernetesresource.ManifestApplyInput,
) (map[string]any, error) {
	// Recorded before the outcome: `applied` counts what was sent to the Cluster,
	// which is what "did it stop at the failure" is a question about.
	access.applied = append(access.applied, input)
	// The same identity check the resource layer makes before sending anything.
	// Mirrored here because a fake that accepts a body disagreeing with the scope
	// of its own request will pass every test while the real one refuses every
	// document — which is exactly how a Namespace resolved but never written back
	// into the document reached an operator as `invalid_request`.
	if objectNamespace(input.Object) != input.Namespace ||
		objectName(input.Object) != input.Name {
		return nil, kubernetesresource.ErrInvalidInput
	}
	if err, failing := access.applyErr[input.Name]; failing {
		return nil, err
	}
	return liveObject("uid-"+input.Name, "1"), nil
}

func objectNamespace(object map[string]any) string {
	metadata, _ := object["metadata"].(map[string]any)
	namespace, _ := metadata["namespace"].(string)
	return namespace
}

func objectName(object map[string]any) string {
	metadata, _ := object["metadata"].(map[string]any)
	name, _ := metadata["name"].(string)
	return name
}

func (access *fakeAccess) DeleteResource(
	_ context.Context,
	input kubernetesresource.ManifestDeleteInput,
) error {
	access.deleted = append(access.deleted, input)
	return nil
}

func (access *fakeAccess) RequirementForApply(
	resource kubernetesresource.ResourceIdentity,
	creating bool,
	targets ...kubernetesresource.ManifestTarget,
) (kubernetesresource.ManifestRequirement, bool, error) {
	return kubernetesresource.NewManifestAccess(nil, access.grant).
		RequirementForApply(resource, creating, targets...)
}

func (access *fakeAccess) RequirementForDelete(
	resource kubernetesresource.ResourceIdentity,
	targets ...kubernetesresource.ManifestTarget,
) (kubernetesresource.ManifestRequirement, bool, error) {
	return kubernetesresource.NewManifestAccess(nil, access.grant).
		RequirementForDelete(resource, targets...)
}
