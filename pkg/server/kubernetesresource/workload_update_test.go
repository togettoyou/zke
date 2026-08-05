package kubernetesresource

import (
	"context"
	"errors"
	"io"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
)

/*
 * A Deployment carrying everything the typed form does not model.
 *
 * These are the fields an operator sets once through YAML and expects to stay
 * set. An edit that changes an image tag must not be the thing that removes
 * them, so the merge is tested against a Pod that has one of each.
 */
func unmodeledDeployment() *appsv1.Deployment {
	replicas := int32(3)
	privileged := false
	runAsUser := int64(1000)
	return &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name: "api", Namespace: "default",
			UID: types.UID("deployment-uid"), ResourceVersion: "12",
			Labels: map[string]string{"app": "api"},
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      map[string]string{"app": "api"},
					Annotations: map[string]string{workloadRestartAnnotation: "abc123"},
				},
				Spec: corev1.PodSpec{
					ServiceAccountName: "api",
					HostNetwork:        true,
					Affinity: &corev1.Affinity{
						PodAntiAffinity: &corev1.PodAntiAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: []corev1.PodAffinityTerm{{
								TopologyKey: "kubernetes.io/hostname",
							}},
						},
					},
					TopologySpreadConstraints: []corev1.TopologySpreadConstraint{{
						MaxSkew: 1, TopologyKey: "topology.kubernetes.io/zone",
					}},
					Volumes: []corev1.Volume{{
						Name: "token",
						VolumeSource: corev1.VolumeSource{
							Projected: &corev1.ProjectedVolumeSource{},
						},
					}},
					Containers: []corev1.Container{{
						Name:  "main",
						Image: "example/api:v1",
						Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
						Env: []corev1.EnvVar{{
							Name: "POD_IP",
							ValueFrom: &corev1.EnvVarSource{
								FieldRef: &corev1.ObjectFieldSelector{FieldPath: "status.podIP"},
							},
						}},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "token", MountPath: "/var/run/token",
							MountPropagation: mountPropagationPointer(corev1.MountPropagationHostToContainer),
						}},
						LivenessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								GRPC: &corev1.GRPCAction{Port: 9000},
							},
						},
						SecurityContext: &corev1.SecurityContext{
							Privileged: &privileged,
							RunAsUser:  &runAsUser,
						},
						TerminationMessagePath: "/dev/termination-log",
					}},
				},
			},
		},
	}
}

func mountPropagationPointer(value corev1.MountPropagationMode) *corev1.MountPropagationMode {
	return &value
}

// The whole reason this update is a merge rather than a replacement.
func TestUpdateWorkloadObjectKeepsFieldsOutsideTheTypedForm(t *testing.T) {
	t.Parallel()

	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(unmodeledDeployment())
	if err != nil {
		t.Fatal(err)
	}
	replicas := int32(5)
	updated, err := updateWorkloadObject(existing, UpdateWorkloadInput{
		Namespace: "default", Resource: WorkloadDeployments, Name: "api",
		UID: "deployment-uid", ResourceVersion: "12",
		WorkloadSpecInput: WorkloadSpecInput{
			Labels:   map[string]string{"app": "api"},
			Replicas: &replicas,
			// Submitted the way a form that read the detail would submit it: the
			// env var and the volume have no source to show, and the probe no
			// handler, because none of the three is modeled.
			Containers: []WorkloadContainerTemplate{{
				Name:  "main",
				Image: "example/api:v2",
				Env:   []WorkloadEnvVar{{Name: "POD_IP"}},
				VolumeMounts: []WorkloadVolumeMount{{
					Name: "token", MountPath: "/var/run/token",
				}},
				LivenessProbe: &WorkloadProbe{},
			}},
			Volumes: []WorkloadVolume{{Name: "token"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var result appsv1.Deployment
	if runtime.DefaultUnstructuredConverter.FromUnstructured(updated, &result) != nil {
		t.Fatal("update produced an object that is not a Deployment")
	}

	container := result.Spec.Template.Spec.Containers[0]
	if container.Image != "example/api:v2" {
		t.Fatalf("image = %q, want the submitted one", container.Image)
	}
	if result.Spec.Replicas == nil || *result.Spec.Replicas != 5 {
		t.Fatalf("replicas = %v, want 5", result.Spec.Replicas)
	}
	if result.Spec.Template.Spec.Affinity == nil ||
		len(result.Spec.Template.Spec.TopologySpreadConstraints) != 1 ||
		result.Spec.Template.Spec.ServiceAccountName != "api" ||
		!result.Spec.Template.Spec.HostNetwork {
		t.Fatalf("Pod-level fields outside the form were dropped: %+v", result.Spec.Template.Spec)
	}
	if len(container.Ports) != 1 || container.TerminationMessagePath == "" {
		t.Fatalf("container fields outside the form were dropped: %+v", container)
	}
	if container.SecurityContext == nil || container.SecurityContext.RunAsUser == nil {
		t.Fatalf("security context outside `privileged` was dropped: %+v", container.SecurityContext)
	}
	if container.SecurityContext.Privileged != nil {
		t.Fatalf("privileged was written as an explicit value: %+v", container.SecurityContext)
	}
	if len(container.Env) != 1 || container.Env[0].ValueFrom == nil ||
		container.Env[0].ValueFrom.FieldRef == nil {
		t.Fatalf("field reference env var was flattened: %+v", container.Env)
	}
	if container.VolumeMounts[0].MountPropagation == nil {
		t.Fatalf("mount fields outside the form were dropped: %+v", container.VolumeMounts[0])
	}
	if container.LivenessProbe == nil || container.LivenessProbe.GRPC == nil {
		t.Fatalf("probe the form cannot express was replaced: %+v", container.LivenessProbe)
	}
	if len(result.Spec.Template.Spec.Volumes) != 1 ||
		result.Spec.Template.Spec.Volumes[0].Projected == nil {
		t.Fatalf("volume source the form cannot express was dropped: %+v", result.Spec.Template.Spec.Volumes)
	}
	// A rolling restart is recorded on the template; removing it would itself
	// change the template and start another rollout.
	if result.Spec.Template.Annotations[workloadRestartAnnotation] != "abc123" {
		t.Fatalf("restart annotation was dropped: %+v", result.Spec.Template.Annotations)
	}
	// The selector cannot change, so the template has to keep matching it.
	if result.Spec.Template.Labels["app"] != "api" {
		t.Fatalf("template stopped matching its selector: %+v", result.Spec.Template.Labels)
	}
}

// Kubernetes rejects every one of these; being refused here is the difference
// between a clear message and a cluster round trip that fails with a 422.
func TestUpdateWorkloadInputRefusesImmutableFields(t *testing.T) {
	t.Parallel()

	base := UpdateWorkloadInput{
		Namespace: "default", Name: "api", UID: "uid", ResourceVersion: "12",
		WorkloadSpecInput: WorkloadSpecInput{
			Containers: []WorkloadContainerTemplate{{Name: "main", Image: "example/api:v2"}},
		},
	}
	testCases := []struct {
		name   string
		mutate func(UpdateWorkloadInput) UpdateWorkloadInput
	}{
		{
			name: "StatefulSet governing Service",
			mutate: func(input UpdateWorkloadInput) UpdateWorkloadInput {
				input.Resource = WorkloadStatefulSets
				input.ServiceName = "api-headless"
				return input
			},
		},
		{
			name: "Job Pod template",
			mutate: func(input UpdateWorkloadInput) UpdateWorkloadInput {
				input.Resource = WorkloadJobs
				return input
			},
		},
		{
			name: "Job completions",
			mutate: func(input UpdateWorkloadInput) UpdateWorkloadInput {
				completions := int32(4)
				input.Resource = WorkloadJobs
				input.Containers = nil
				input.Completions = &completions
				return input
			},
		},
		{
			name: "missing preconditions",
			mutate: func(input UpdateWorkloadInput) UpdateWorkloadInput {
				input.Resource = WorkloadDeployments
				input.ResourceVersion = ""
				return input
			},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := validateUpdateWorkloadInput(testCase.mutate(base)); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("error = %v, want invalid input", err)
			}
		})
	}
}

// A Job accepts exactly the two fields Kubernetes lets move.
func TestUpdateJobAcceptsOnlyItsMutableFields(t *testing.T) {
	t.Parallel()

	parallelism := int32(4)
	ttl := int32(600)
	existing, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&batchv1.Job{
		TypeMeta:   metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{Name: "finetune", Namespace: "default"},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "main", Image: "example/train:v1"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	input := UpdateWorkloadInput{
		Namespace: "default", Resource: WorkloadJobs, Name: "finetune",
		UID: "job-uid", ResourceVersion: "3",
		WorkloadSpecInput: WorkloadSpecInput{
			Parallelism: &parallelism, TTLSecondsAfterFinished: &ttl,
		},
	}
	if err := validateUpdateWorkloadInput(input); err != nil {
		t.Fatalf("valid Job update rejected: %v", err)
	}
	updated, err := updateWorkloadObject(existing, input)
	if err != nil {
		t.Fatal(err)
	}
	var result batchv1.Job
	if runtime.DefaultUnstructuredConverter.FromUnstructured(updated, &result) != nil {
		t.Fatal("update produced an object that is not a Job")
	}
	if result.Spec.Parallelism == nil || *result.Spec.Parallelism != 4 ||
		result.Spec.TTLSecondsAfterFinished == nil || *result.Spec.TTLSecondsAfterFinished != 600 {
		t.Fatalf("mutable Job fields were not applied: %+v", result.Spec)
	}
	if result.Spec.Template.Spec.Containers[0].Image != "example/train:v1" {
		t.Fatalf("immutable Job template was changed: %+v", result.Spec.Template.Spec.Containers)
	}
}

// An edit is filled in against one version of an object.
func TestUpdateWorkloadRejectsStaleIdentityBeforeMutation(t *testing.T) {
	t.Parallel()

	requester := &fakeResourceRequester{
		handle: func(_ context.Context, _ string, request *agentv1.ResourceRequest, responseBody io.Writer) (*agentv1.ResourceResponse, error) {
			if request.GetVerb() != agentv1.ResourceVerb_RESOURCE_VERB_GET {
				t.Fatalf("unexpected request: %+v", request)
			}
			return writeKubernetesObject(t, responseBody, unmodeledDeployment()), nil
		},
		mutate: func(context.Context, string, *agentv1.ResourceRequest, io.Reader, io.Writer, string) (*agentv1.ResourceResponse, error) {
			t.Fatal("stale update reached mutation transport")
			return nil, nil
		},
	}
	_, err := NewService(requester).UpdateWorkload(context.Background(), UpdateWorkloadInput{
		ClusterID: testClusterID, Namespace: "default", Resource: WorkloadDeployments, Name: "api",
		UID: "deployment-uid", ResourceVersion: "11",
		WorkloadSpecInput: WorkloadSpecInput{
			Containers: []WorkloadContainerTemplate{{Name: "main", Image: "example/api:v2"}},
		},
		Confirm: true, IdempotencyKey: "workload-update-0001",
	})
	if !errors.Is(err, ErrUpstreamConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
}

// The detail an edit form opens on has to carry what the form submits back.
func TestWorkloadDetailReturnsTheTypedTemplate(t *testing.T) {
	t.Parallel()

	object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&appsv1.Deployment{
		TypeMeta:   metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "default"},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					NodeSelector:     map[string]string{"gpu": "true"},
					ImagePullSecrets: []corev1.LocalObjectReference{{Name: "registry"}},
					Tolerations: []corev1.Toleration{{
						Key: "nvidia.com/gpu", Operator: corev1.TolerationOpExists,
					}},
					Volumes: []corev1.Volume{{
						Name: "config",
						VolumeSource: corev1.VolumeSource{
							ConfigMap: &corev1.ConfigMapVolumeSource{
								LocalObjectReference: corev1.LocalObjectReference{Name: "api-config"},
							},
						},
					}},
					Containers: []corev1.Container{{
						Name:  "main",
						Image: "example/api:v1",
						Env:   []corev1.EnvVar{{Name: "LOG_LEVEL", Value: "debug"}},
						VolumeMounts: []corev1.VolumeMount{{
							Name: "config", MountPath: "/etc/api",
						}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler: corev1.ProbeHandler{
								HTTPGet: &corev1.HTTPGetAction{
									Path: "/healthz", Port: intstr.FromInt32(8080),
								},
							},
							PeriodSeconds: 10,
						},
					}},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := workloadDetail(object, WorkloadDeployments, "default", "api")
	if err != nil {
		t.Fatal(err)
	}
	container := detail.Containers[0]
	if len(container.Env) != 1 || container.Env[0].Value != "debug" ||
		len(container.VolumeMounts) != 1 || container.VolumeMounts[0].MountPath != "/etc/api" {
		t.Fatalf("container template is incomplete: %+v", container)
	}
	if container.ReadinessProbe == nil ||
		container.ReadinessProbe.HTTPGet == nil ||
		container.ReadinessProbe.HTTPGet.Port != "8080" ||
		container.ReadinessProbe.PeriodSeconds == nil ||
		*container.ReadinessProbe.PeriodSeconds != 10 {
		t.Fatalf("probe is incomplete: %+v", container.ReadinessProbe)
	}
	// Zero is how Kubernetes reports a timing it defaulted, and submitting an
	// explicit zero back would be rejected.
	if container.ReadinessProbe.SuccessThreshold != nil {
		t.Fatalf("defaulted probe timing came back as an explicit value: %+v", container.ReadinessProbe)
	}
	if len(detail.Volumes) != 1 || detail.Volumes[0].ConfigMap == nil ||
		detail.Volumes[0].ConfigMap.Name != "api-config" {
		t.Fatalf("volumes are incomplete: %+v", detail.Volumes)
	}
	if len(detail.ImagePullSecrets) != 1 || detail.NodeSelector["gpu"] != "true" ||
		len(detail.Tolerations) != 1 || detail.Tolerations[0].Operator != "Exists" {
		t.Fatalf("Pod-level template fields are incomplete: %+v", detail)
	}
}
