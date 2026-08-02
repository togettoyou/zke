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
	storagev1 "k8s.io/api/storage/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
)

func TestCreateStorageObjectsCoversPVCPVAndStorageClass(t *testing.T) {
	t.Parallel()

	pvObject, err := createStorageObject(CreateStorageResourceInput{
		Resource: StoragePersistentVolumes, Name: "csi-volume",
		PersistentVolume: &PersistentVolumeCreateSpec{
			Capacity: "20Gi", AccessModes: []string{"ReadWriteOnce"}, StorageClassName: "fast",
			Source: &PersistentVolumeSourceView{Type: "csi", CSI: &CSIPersistentVolumeSource{
				Driver: "csi.example.com", VolumeHandle: "volume-0001",
				VolumeAttributes:     map[string]string{"opaque key": "value"},
				NodePublishSecretRef: &StorageSecretReference{Namespace: "storage-system", Name: "csi-credentials"},
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var pv corev1.PersistentVolume
	if runtime.DefaultUnstructuredConverter.FromUnstructured(pvObject, &pv) != nil ||
		pv.Spec.Capacity.Storage().Cmp(resource.MustParse("20Gi")) != 0 || pv.Spec.CSI == nil ||
		pv.Spec.CSI.NodePublishSecretRef.Name != "csi-credentials" ||
		pv.Spec.PersistentVolumeReclaimPolicy != corev1.PersistentVolumeReclaimRetain {
		t.Fatalf("unexpected PersistentVolume: %+v", pv)
	}

	disabledClass := ""
	pvcObject, err := createStorageObject(CreateStorageResourceInput{
		Resource: StoragePersistentVolumeClaims, Namespace: "default", Name: "data",
		PersistentVolumeClaim: &PersistentVolumeClaimCreateSpec{
			RequestedCapacity: "5Gi", AccessModes: []string{"ReadWriteOnce"}, StorageClassName: &disabledClass,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var pvc corev1.PersistentVolumeClaim
	if runtime.DefaultUnstructuredConverter.FromUnstructured(pvcObject, &pvc) != nil ||
		pvc.Spec.StorageClassName == nil || *pvc.Spec.StorageClassName != "" ||
		pvc.Spec.Resources.Requests.Storage().Cmp(resource.MustParse("5Gi")) != 0 {
		t.Fatalf("unexpected PersistentVolumeClaim: %+v", pvc)
	}

	classObject, err := createStorageObject(CreateStorageResourceInput{
		Resource: StorageClasses, Name: "fast",
		StorageClass: &StorageClassCreateSpec{Provisioner: "csi.example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var class storagev1.StorageClass
	if runtime.DefaultUnstructuredConverter.FromUnstructured(classObject, &class) != nil ||
		class.ReclaimPolicy == nil || *class.ReclaimPolicy != corev1.PersistentVolumeReclaimDelete ||
		class.VolumeBindingMode == nil || *class.VolumeBindingMode != storagev1.VolumeBindingImmediate ||
		class.AllowVolumeExpansion == nil || *class.AllowVolumeExpansion {
		t.Fatalf("unexpected StorageClass: %+v", class)
	}
}

func TestPersistentVolumeCreationValidatesSourceAndLocalAffinity(t *testing.T) {
	t.Parallel()

	inputs := []CreateStorageResourceInput{
		{
			Resource: StoragePersistentVolumes, Name: "ambiguous",
			PersistentVolume: &PersistentVolumeCreateSpec{
				Capacity: "1Gi", AccessModes: []string{"ReadWriteOnce"},
				Source: &PersistentVolumeSourceView{CSI: &CSIPersistentVolumeSource{Driver: "csi.example.com", VolumeHandle: "one"}, NFS: &NFSPersistentVolumeSource{Server: "nfs", Path: "/data"}},
			},
		},
		{
			Resource: StoragePersistentVolumes, Name: "local",
			PersistentVolume: &PersistentVolumeCreateSpec{
				Capacity: "1Gi", AccessModes: []string{"ReadWriteOnce"},
				Source: &PersistentVolumeSourceView{Local: &LocalPersistentVolumeSource{Path: "/mnt/data"}},
			},
		},
		{
			Resource: StoragePersistentVolumes, Name: "block-nfs",
			PersistentVolume: &PersistentVolumeCreateSpec{
				Capacity: "1Gi", AccessModes: []string{"ReadWriteOnce"}, VolumeMode: "Block",
				Source: &PersistentVolumeSourceView{NFS: &NFSPersistentVolumeSource{Server: "nfs", Path: "/data"}},
			},
		},
	}
	for index, input := range inputs {
		if _, err := createStorageObject(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d error = %v, want ErrInvalidInput", index, err)
		}
	}
}

func TestStorageResourceDetailsUseTypedStatusAndEmptyCollections(t *testing.T) {
	t.Parallel()

	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: types.UID("claim-uid"), ResourceVersion: "6"},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources:   corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("5Gi")}},
		},
		Status: corev1.PersistentVolumeClaimStatus{
			Phase: corev1.ClaimBound, Capacity: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		},
	}
	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pvc)
	if err != nil {
		t.Fatal(err)
	}
	detail, err := storageResourceDetail(object, StoragePersistentVolumeClaims, "default", "data")
	if err != nil {
		t.Fatal(err)
	}
	if detail.PersistentVolumeClaimDetail == nil || detail.PersistentVolumeClaim == nil || detail.PersistentVolumeClaim.Phase != "Bound" ||
		detail.PersistentVolumeClaim.RequestedCapacity != "5Gi" || detail.PersistentVolumeClaim.Capacity != "10Gi" {
		t.Fatalf("unexpected PVC detail: %+v", detail)
	}
	body, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	for _, unexpected := range []string{`"labels":null`, `"annotations":null`, `"access_modes":null`, `"conditions":null`} {
		if strings.Contains(string(body), unexpected) {
			t.Fatalf("storage detail contains %s: %s", unexpected, body)
		}
	}
}

func TestPersistentVolumeClaimUpdateRejectsShrink(t *testing.T) {
	t.Parallel()

	pvc := &corev1.PersistentVolumeClaim{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolumeClaim"},
		ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "default", UID: types.UID("claim-uid"), ResourceVersion: "6"},
		Spec: corev1.PersistentVolumeClaimSpec{Resources: corev1.VolumeResourceRequirements{
			Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("10Gi")},
		}},
	}
	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pvc)
	if err != nil {
		t.Fatal(err)
	}
	_, err = updateStorageObject(existing, UpdateStorageResourceInput{
		Resource: StoragePersistentVolumeClaims, Namespace: "default", Name: "data",
		PersistentVolumeClaim: &PersistentVolumeClaimUpdateSpec{RequestedCapacity: "5Gi"},
	})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
}

func TestUpdateStorageResourceRejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	pv := &corev1.PersistentVolume{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "PersistentVolume"},
		ObjectMeta: metav1.ObjectMeta{Name: "volume", UID: types.UID("current-uid"), ResourceVersion: "9"},
	}
	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, pv), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdateStorageResource(context.Background(), UpdateStorageResourceInput{
		ClusterID: testClusterID, Resource: StoragePersistentVolumes, Name: "volume",
		UID: "stale-uid", ResourceVersion: "9", PersistentVolume: &PersistentVolumeUpdateSpec{ReclaimPolicy: "Retain"},
		Confirm: true, IdempotencyKey: "storage-update-0001",
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want ErrUpstreamConflict", err)
	}
}
