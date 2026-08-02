package kubernetesresource

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestConfigMapObjectRoundTrip(t *testing.T) {
	t.Parallel()

	object, err := createConfigMapObject(CreateConfigMapInput{
		Namespace: "default", Name: "app-config",
		Labels: map[string]string{"app": "api"}, Annotations: map[string]string{"example.com/owner": "platform"},
		Data:       map[string]string{"z.yaml": "enabled: true\n", "a.env": "PORT=8080"},
		BinaryData: map[string]string{"logo.bin": "AAEC"}, Immutable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := configMapDetail(object, "default", "app-config")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Name != "app-config" || !detail.Immutable || detail.Labels["app"] != "api" ||
		detail.Annotations["example.com/owner"] != "platform" || detail.BinaryData["logo.bin"] != "AAEC" ||
		detail.BinaryDataBytes != 3 || detail.DataBytes != int64(len("enabled: true\n")+len("PORT=8080")) {
		t.Fatalf("unexpected ConfigMap detail: %+v", detail)
	}
	if len(detail.DataKeys) != 2 || detail.DataKeys[0] != "a.env" || detail.DataKeys[1] != "z.yaml" {
		t.Fatalf("data keys are not sorted: %v", detail.DataKeys)
	}
}

func TestConfigMapDetailUsesEmptyCollections(t *testing.T) {
	t.Parallel()

	object, err := createConfigMapObject(CreateConfigMapInput{Namespace: "default", Name: "empty"})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := configMapDetail(object, "default", "empty")
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, unexpected := range []string{`"labels":null`, `"annotations":null`, `"data":null`, `"binary_data":null`, `"data_keys":null`, `"binary_data_keys":null`} {
		if strings.Contains(string(body), unexpected) {
			t.Fatalf("ConfigMap detail contains %s: %s", unexpected, body)
		}
	}
}

func TestConfigMapInputValidation(t *testing.T) {
	t.Parallel()

	tests := []CreateConfigMapInput{
		{Namespace: "default", Name: "config", Data: map[string]string{"same": "text"}, BinaryData: map[string]string{"same": "AA=="}},
		{Namespace: "default", Name: "config", BinaryData: map[string]string{"blob": "not-base64"}},
		{Namespace: "default", Name: "config", Data: map[string]string{"bad/key": "value"}},
		{Namespace: "default", Name: "config", Data: map[string]string{"data": strings.Repeat("x", maxConfigMapSize+1)}},
	}
	for index, input := range tests {
		if _, err := createConfigMapObject(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d error = %v, want ErrInvalidInput", index, err)
		}
	}
}

func TestConfigMapAcceptsKubernetesMaximumValueSize(t *testing.T) {
	t.Parallel()

	input := CreateConfigMapInput{
		Namespace: "default", Name: "maximum", Data: map[string]string{"data": strings.Repeat("x", maxConfigMapSize)},
	}
	if !validConfigMapMetadata(input.Namespace, input.Name, input.Labels, input.Annotations) {
		t.Fatal("maximum-size ConfigMap metadata is invalid")
	}
	if _, err := decodeConfigMapBinaryData(input.Data, input.BinaryData); err != nil {
		t.Fatalf("maximum-size ConfigMap data error = %v", err)
	}
	if _, err := createConfigMapObject(input); err != nil {
		t.Fatalf("maximum-size ConfigMap error = %v", err)
	}
}

func TestUpdateConfigMapRejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "app-config", Namespace: "default", UID: types.UID("current-uid"), ResourceVersion: "8",
		},
		Data: map[string]string{"mode": "safe"},
	}
	requester := &fakeResourceRequester{
		handle: func(
			_ context.Context,
			_ string,
			request *agentv1.ResourceRequest,
			responseBody io.Writer,
		) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, configMap), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdateConfigMap(context.Background(), UpdateConfigMapInput{
		ClusterID: testClusterID, Namespace: "default", Name: "app-config",
		UID: "stale-uid", ResourceVersion: "8", Data: map[string]string{"mode": "safe"},
		BinaryData: map[string]string{}, Confirm: true, IdempotencyKey: "config-map-update-0001",
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

func TestImmutableConfigMapRejectsContentChanges(t *testing.T) {
	t.Parallel()

	existing, err := createConfigMapObject(CreateConfigMapInput{
		Namespace: "default", Name: "locked", Data: map[string]string{"mode": "safe"}, Immutable: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata := existing["metadata"].(map[string]any)
	metadata["uid"] = "config-uid"
	metadata["resourceVersion"] = "7"
	input := UpdateConfigMapInput{
		Namespace: "default", Name: "locked", UID: "config-uid", ResourceVersion: "7",
		Data: map[string]string{"mode": "unsafe"}, BinaryData: map[string]string{},
	}
	binaryData, err := decodeConfigMapBinaryData(input.Data, input.BinaryData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateConfigMapObject(existing, input, binaryData); !errors.Is(err, ErrConfigMapImmutable) {
		t.Fatalf("error = %v, want ErrConfigMapImmutable", err)
	}

	input.Data["mode"] = "safe"
	binaryData, err = decodeConfigMapBinaryData(input.Data, input.BinaryData)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := updateConfigMapObject(existing, input, binaryData); err != nil {
		t.Fatalf("unchanged immutable ConfigMap error = %v", err)
	}
}
