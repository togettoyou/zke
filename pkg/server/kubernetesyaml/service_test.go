package kubernetesyaml

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

const testClusterID = "01234567-89ab-cdef-0123-456789abcdef"

var testResource = kubernetesresource.ResourceIdentity{
	Group: "apps", Version: "v1", Resource: "deployments",
}

func TestServiceGetReturnsYAML(t *testing.T) {
	t.Parallel()
	resources := &fakeResourceService{
		get: func(_ context.Context, input kubernetesresource.GetResourceInput) (map[string]any, error) {
			if input.ClusterID != testClusterID || input.Resource != testResource ||
				input.Namespace != "team-a" || input.Name != "api" {
				t.Fatalf("unexpected get input: %+v", input)
			}
			return deploymentObject("uid-1", "42"), nil
		},
	}
	result, err := NewService(resources, nil).Get(context.Background(), GetInput{
		ClusterID: testClusterID,
		Resource:  testResource,
		Namespace: "team-a",
		Name:      "api",
	})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if result.UID != "uid-1" || result.ResourceVersion != "42" ||
		!strings.Contains(string(result.Manifest), "apiVersion: apps/v1\n") ||
		!strings.HasSuffix(string(result.Manifest), "\n") {
		t.Fatalf("unexpected result: %+v\n%s", result, result.Manifest)
	}
}

func TestServiceUpdateValidatesIdentityAndForwardsOptions(t *testing.T) {
	t.Parallel()
	resources := &fakeResourceService{
		get: func(context.Context, kubernetesresource.GetResourceInput) (map[string]any, error) {
			return deploymentObject("uid-1", "42"), nil
		},
		update: func(_ context.Context, input kubernetesresource.UpdateResourceInput) (map[string]any, error) {
			if input.Object["kind"] != "Deployment" || !input.Options.DryRun ||
				input.Options.FieldManager != "zke-yaml" || input.Confirm ||
				input.IdempotencyKey != "0123456789abcdef" {
				t.Fatalf("unexpected update input: %+v", input)
			}
			return input.Object, nil
		},
	}
	result, err := NewService(resources, nil).Update(context.Background(), UpdateInput{
		GetInput: GetInput{
			ClusterID: testClusterID,
			Resource:  testResource,
			Namespace: "team-a",
			Name:      "api",
		},
		Manifest: []byte(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
  namespace: team-a
  uid: uid-1
  resourceVersion: "42"
spec:
  replicas: 2
`),
		DryRun:         true,
		FieldManager:   "zke-yaml",
		IdempotencyKey: "0123456789abcdef",
	})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.UID != "uid-1" || result.ResourceVersion != "42" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestServiceUpdateRejectsStaleIdentity(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name    string
		uid     string
		version string
		want    error
	}{
		{name: "uid", uid: "old-uid", version: "42", want: ErrResourceUIDChanged},
		{name: "version", uid: "uid-1", version: "41", want: ErrResourceVersionChanged},
		{name: "missing uid", uid: "", version: "42", want: ErrResourceUIDChanged},
		{name: "missing version", uid: "uid-1", version: "", want: ErrResourceVersionChanged},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resources := &fakeResourceService{
				get: func(context.Context, kubernetesresource.GetResourceInput) (map[string]any, error) {
					return deploymentObject("uid-1", "42"), nil
				},
				update: func(context.Context, kubernetesresource.UpdateResourceInput) (map[string]any, error) {
					t.Fatal("UpdateResource() called for stale identity")
					return nil, nil
				},
			}
			manifest := "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n" +
				"  name: api\n  namespace: team-a\n"
			if testCase.uid != "" {
				manifest += "  uid: " + testCase.uid + "\n"
			}
			if testCase.version != "" {
				manifest += "  resourceVersion: \"" + testCase.version + "\"\n"
			}
			_, err := NewService(resources, nil).Update(context.Background(), UpdateInput{
				GetInput: GetInput{
					ClusterID: testClusterID, Resource: testResource,
					Namespace: "team-a", Name: "api",
				},
				Manifest: manifestBytes(manifest),
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("Update() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestServiceUpdateRejectsInvalidYAML(t *testing.T) {
	t.Parallel()
	testCases := []struct {
		name     string
		manifest string
	}{
		{name: "empty", manifest: "  \n"},
		{name: "multiple documents", manifest: "apiVersion: v1\n---\nkind: Pod\n"},
		{name: "duplicate key", manifest: "apiVersion: v1\napiVersion: v2\n"},
		{name: "sequence root", manifest: "- apiVersion: v1\n"},
		{name: "alias", manifest: "metadata: &metadata\n  name: api\ncopy: *metadata\n"},
		{name: "identity mismatch", manifest: "apiVersion: apps/v1\nkind: Deployment\nmetadata:\n  name: other\n  namespace: team-a\n  uid: uid-1\n  resourceVersion: \"42\"\n"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			resources := &fakeResourceService{
				get: func(context.Context, kubernetesresource.GetResourceInput) (map[string]any, error) {
					return deploymentObject("uid-1", "42"), nil
				},
			}
			_, err := NewService(resources, nil).Update(context.Background(), UpdateInput{
				GetInput: GetInput{
					ClusterID: testClusterID, Resource: testResource,
					Namespace: "team-a", Name: "api",
				},
				Manifest: manifestBytes(testCase.manifest),
			})
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("Update() error = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

func deploymentObject(uid string, resourceVersion string) map[string]any {
	metadata := map[string]any{
		"name": "api", "namespace": "team-a", "uid": uid,
		"resourceVersion": resourceVersion,
	}
	return map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   metadata,
		"spec":       map[string]any{"replicas": float64(2)},
	}
}

func manifestBytes(value string) []byte {
	return []byte(value)
}

type fakeResourceService struct {
	get    func(context.Context, kubernetesresource.GetResourceInput) (map[string]any, error)
	update func(context.Context, kubernetesresource.UpdateResourceInput) (map[string]any, error)
}

func (service *fakeResourceService) GetResource(
	ctx context.Context,
	input kubernetesresource.GetResourceInput,
) (map[string]any, error) {
	return service.get(ctx, input)
}

func (service *fakeResourceService) UpdateResource(
	ctx context.Context,
	input kubernetesresource.UpdateResourceInput,
) (map[string]any, error) {
	return service.update(ctx, input)
}
