package kubernetesresource

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
)

func minimalWorkloadInput() CreateWorkloadInput {
	return CreateWorkloadInput{
		ClusterID: testClusterID,
		Namespace: "model-serving",
		Resource:  WorkloadDeployments,
		Name:      "gateway",
		Containers: []WorkloadContainerTemplate{{
			Name: "main", Image: "example/gateway:v1",
		}},
	}
}

// The Pod template is where a typed create stops being a name and an image, so
// every field it accepts has to survive the round trip into the object that is
// actually sent.
func TestCreateWorkloadObjectCarriesThePodTemplate(t *testing.T) {
	t.Parallel()

	optional := true
	privileged := true
	mode := int32(0644)
	period := int32(10)
	tolerationSeconds := int64(30)
	input := minimalWorkloadInput()
	input.Annotations = map[string]string{"owner": "platform"}
	input.Description = "边缘网关"
	input.Containers = []WorkloadContainerTemplate{{
		Name:            "main",
		Image:           "example/gateway:v1",
		ImagePullPolicy: "IfNotPresent",
		Command:         []string{"/bin/gateway"},
		Args:            []string{"--listen", ":8080"},
		WorkingDir:      "/srv",
		Env: []WorkloadEnvVar{
			{Name: "LOG_LEVEL", Value: "info"},
			{Name: "TOKEN", SecretKeyRef: &WorkloadObjectKeyRef{
				Name: "gateway-secret", Key: "token", Optional: &optional,
			}},
		},
		Resources: &WorkloadResourceRequirements{
			Requests: map[string]string{"cpu": "250m", "memory": "256Mi"},
			Limits:   map[string]string{"cpu": "500m", "nvidia.com/gpu": "1"},
		},
		VolumeMounts: []WorkloadVolumeMount{{
			Name: "config", MountPath: "/etc/gateway", SubPath: "gateway.yaml", ReadOnly: true,
		}},
		LivenessProbe: &WorkloadProbe{
			HTTPGet:       &WorkloadHTTPGetAction{Path: "/healthz", Port: "8080", Scheme: "HTTP"},
			PeriodSeconds: &period,
		},
		ReadinessProbe: &WorkloadProbe{TCPSocket: &WorkloadTCPSocketAction{Port: "http"}},
		Lifecycle: &WorkloadLifecycle{
			PreStop: &WorkloadLifecycleHandler{
				Exec: &WorkloadExecAction{Command: []string{"/bin/drain"}},
			},
		},
		Privileged: &privileged,
	}}
	input.Volumes = []WorkloadVolume{{
		Name:      "config",
		ConfigMap: &WorkloadConfigMapVolume{Name: "gateway-config", DefaultMode: &mode},
	}}
	input.ImagePullSecrets = []string{"registry-key"}
	input.NodeSelector = map[string]string{"kubernetes.io/os": "linux"}
	input.Tolerations = []WorkloadToleration{{
		Key: "gpu", Operator: "Equal", Value: "true",
		Effect: "NoExecute", TolerationSeconds: &tolerationSeconds,
	}}

	if err := validateCreateWorkloadInput(input); err != nil {
		t.Fatalf("rejected a valid Pod template: %v", err)
	}
	object, err := createWorkloadObject(input)
	if err != nil {
		t.Fatal(err)
	}
	deployment, ok := object.(*appsv1.Deployment)
	if !ok {
		t.Fatalf("unexpected object type %T", object)
	}

	if deployment.Annotations[workloadDescriptionAnnotation] != "边缘网关" ||
		deployment.Annotations["owner"] != "platform" {
		t.Fatalf("unexpected workload annotations: %+v", deployment.Annotations)
	}
	// The Pod carries them too: a description is about the thing that runs.
	template := deployment.Spec.Template
	if template.Annotations[workloadDescriptionAnnotation] != "边缘网关" {
		t.Fatalf("unexpected Pod annotations: %+v", template.Annotations)
	}

	container := template.Spec.Containers[0]
	gpu := container.Resources.Limits["nvidia.com/gpu"]
	if container.WorkingDir != "/srv" ||
		len(container.Command) != 1 ||
		len(container.Args) != 2 ||
		container.Resources.Requests.Cpu().String() != "250m" ||
		gpu.String() != "1" {
		t.Fatalf("unexpected container: %+v", container)
	}
	if len(container.Env) != 2 ||
		container.Env[0].Value != "info" ||
		container.Env[1].ValueFrom == nil ||
		container.Env[1].ValueFrom.SecretKeyRef.Key != "token" ||
		container.Env[1].Value != "" {
		t.Fatalf("unexpected env: %+v", container.Env)
	}
	if container.LivenessProbe.HTTPGet == nil ||
		container.LivenessProbe.HTTPGet.Port.IntValue() != 8080 ||
		container.LivenessProbe.PeriodSeconds != 10 ||
		container.ReadinessProbe.TCPSocket == nil ||
		container.ReadinessProbe.TCPSocket.Port.StrVal != "http" {
		t.Fatalf("unexpected probes: %+v", container)
	}
	if container.Lifecycle == nil ||
		container.Lifecycle.PostStart != nil ||
		container.Lifecycle.PreStop.Exec == nil {
		t.Fatalf("unexpected lifecycle: %+v", container.Lifecycle)
	}
	if container.SecurityContext == nil || !*container.SecurityContext.Privileged {
		t.Fatalf("unexpected security context: %+v", container.SecurityContext)
	}
	if len(container.VolumeMounts) != 1 || container.VolumeMounts[0].SubPath != "gateway.yaml" {
		t.Fatalf("unexpected volume mounts: %+v", container.VolumeMounts)
	}

	spec := template.Spec
	if len(spec.Volumes) != 1 || spec.Volumes[0].ConfigMap == nil ||
		spec.Volumes[0].ConfigMap.Name != "gateway-config" {
		t.Fatalf("unexpected volumes: %+v", spec.Volumes)
	}
	if len(spec.ImagePullSecrets) != 1 || spec.ImagePullSecrets[0].Name != "registry-key" ||
		spec.NodeSelector["kubernetes.io/os"] != "linux" ||
		len(spec.Tolerations) != 1 || *spec.Tolerations[0].TolerationSeconds != 30 {
		t.Fatalf("unexpected Pod scheduling: %+v", spec)
	}
}

// A container with no explicit security context must not gain one: writing
// `privileged: false` makes an ordinary container look deliberately configured.
func TestCreateWorkloadObjectOmitsUnsetOptionalFields(t *testing.T) {
	t.Parallel()

	unprivileged := false
	input := minimalWorkloadInput()
	input.Containers[0].Privileged = &unprivileged

	object, err := createWorkloadObject(input)
	if err != nil {
		t.Fatal(err)
	}
	deployment := object.(*appsv1.Deployment)
	container := deployment.Spec.Template.Spec.Containers[0]
	if container.SecurityContext != nil ||
		container.Env != nil ||
		container.VolumeMounts != nil ||
		container.LivenessProbe != nil ||
		container.Lifecycle != nil {
		t.Fatalf("unexpected container defaults: %+v", container)
	}
	spec := deployment.Spec.Template.Spec
	if spec.Volumes != nil || spec.ImagePullSecrets != nil || spec.Tolerations != nil {
		t.Fatalf("unexpected Pod defaults: %+v", spec)
	}
	if deployment.Annotations != nil {
		t.Fatalf("unexpected annotations: %+v", deployment.Annotations)
	}
}

func TestValidateCreateWorkloadInputRejectsInvalidPodTemplates(t *testing.T) {
	t.Parallel()

	negative := int64(-1)
	zero := int32(0)
	cases := map[string]func(*CreateWorkloadInput){
		"a probe with no handler": func(input *CreateWorkloadInput) {
			input.Containers[0].LivenessProbe = &WorkloadProbe{}
		},
		"a probe with two handlers": func(input *CreateWorkloadInput) {
			input.Containers[0].LivenessProbe = &WorkloadProbe{
				HTTPGet:   &WorkloadHTTPGetAction{Port: "80"},
				TCPSocket: &WorkloadTCPSocketAction{Port: "80"},
			}
		},
		"a probe period of zero": func(input *CreateWorkloadInput) {
			input.Containers[0].LivenessProbe = &WorkloadProbe{
				TCPSocket: &WorkloadTCPSocketAction{Port: "80"}, PeriodSeconds: &zero,
			}
		},
		"a probe port out of range": func(input *CreateWorkloadInput) {
			input.Containers[0].LivenessProbe = &WorkloadProbe{
				TCPSocket: &WorkloadTCPSocketAction{Port: "70000"},
			}
		},
		"an env var with both a value and a reference": func(input *CreateWorkloadInput) {
			input.Containers[0].Env = []WorkloadEnvVar{{
				Name:  "TOKEN",
				Value: "literal",
				SecretKeyRef: &WorkloadObjectKeyRef{
					Name: "secret", Key: "token",
				},
			}}
		},
		"a duplicate env var name": func(input *CreateWorkloadInput) {
			input.Containers[0].Env = []WorkloadEnvVar{
				{Name: "MODE", Value: "a"}, {Name: "MODE", Value: "b"},
			}
		},
		"a negative resource quantity": func(input *CreateWorkloadInput) {
			input.Containers[0].Resources = &WorkloadResourceRequirements{
				Limits: map[string]string{"cpu": "-1"},
			}
		},
		"a mount of a volume that was not declared": func(input *CreateWorkloadInput) {
			input.Containers[0].VolumeMounts = []WorkloadVolumeMount{{
				Name: "missing", MountPath: "/data",
			}}
		},
		"two mounts at one path": func(input *CreateWorkloadInput) {
			input.Containers[0].VolumeMounts = []WorkloadVolumeMount{
				{Name: "a", MountPath: "/data"}, {Name: "b", MountPath: "/data"},
			}
			input.Volumes = []WorkloadVolume{
				{Name: "a", EmptyDir: &WorkloadEmptyDirVolume{}},
				{Name: "b", EmptyDir: &WorkloadEmptyDirVolume{}},
			}
		},
		"an absolute subPath": func(input *CreateWorkloadInput) {
			input.Containers[0].VolumeMounts = []WorkloadVolumeMount{{
				Name: "data", MountPath: "/data", SubPath: "/etc/passwd",
			}}
			input.Volumes = []WorkloadVolume{{Name: "data", EmptyDir: &WorkloadEmptyDirVolume{}}}
		},
		"a volume with no source": func(input *CreateWorkloadInput) {
			input.Volumes = []WorkloadVolume{{Name: "data"}}
		},
		"a volume with two sources": func(input *CreateWorkloadInput) {
			input.Volumes = []WorkloadVolume{{
				Name:     "data",
				EmptyDir: &WorkloadEmptyDirVolume{},
				HostPath: &WorkloadHostPathVolume{Path: "/var/data"},
			}}
		},
		"a relative hostPath": func(input *CreateWorkloadInput) {
			input.Volumes = []WorkloadVolume{{
				Name: "data", HostPath: &WorkloadHostPathVolume{Path: "var/data"},
			}}
		},
		"a toleration value under Exists": func(input *CreateWorkloadInput) {
			input.Tolerations = []WorkloadToleration{{
				Key: "gpu", Operator: "Exists", Value: "true",
			}}
		},
		"a toleration period without NoExecute": func(input *CreateWorkloadInput) {
			seconds := int64(30)
			input.Tolerations = []WorkloadToleration{{
				Key: "gpu", Operator: "Equal", Value: "true",
				Effect: "NoSchedule", TolerationSeconds: &seconds,
			}}
		},
		"a negative toleration period": func(input *CreateWorkloadInput) {
			input.Tolerations = []WorkloadToleration{{
				Key: "gpu", Operator: "Equal", Value: "true",
				Effect: "NoExecute", TolerationSeconds: &negative,
			}}
		},
		"an empty toleration key without Exists": func(input *CreateWorkloadInput) {
			input.Tolerations = []WorkloadToleration{{Operator: "Equal", Effect: "NoSchedule"}}
		},
		"a description annotation set by hand": func(input *CreateWorkloadInput) {
			input.Annotations = map[string]string{workloadDescriptionAnnotation: "x"}
		},
		"a lifecycle handler with no action": func(input *CreateWorkloadInput) {
			input.Containers[0].Lifecycle = &WorkloadLifecycle{
				PostStart: &WorkloadLifecycleHandler{},
			}
		},
		"an exec action with no command": func(input *CreateWorkloadInput) {
			input.Containers[0].LivenessProbe = &WorkloadProbe{Exec: &WorkloadExecAction{}}
		},
		"a subPath climbing out of its volume": func(input *CreateWorkloadInput) {
			input.Containers[0].VolumeMounts = []WorkloadVolumeMount{{
				Name: "data", MountPath: "/data", SubPath: "../../etc",
			}}
			input.Volumes = []WorkloadVolume{{Name: "data", EmptyDir: &WorkloadEmptyDirVolume{}}}
		},
		"a relative mount path": func(input *CreateWorkloadInput) {
			input.Containers[0].VolumeMounts = []WorkloadVolumeMount{{
				Name: "data", MountPath: "data",
			}}
			input.Volumes = []WorkloadVolume{{Name: "data", EmptyDir: &WorkloadEmptyDirVolume{}}}
		},
		"a mount path carrying a colon": func(input *CreateWorkloadInput) {
			input.Containers[0].VolumeMounts = []WorkloadVolumeMount{{
				Name: "data", MountPath: "/data:/host",
			}}
			input.Volumes = []WorkloadVolume{{Name: "data", EmptyDir: &WorkloadEmptyDirVolume{}}}
		},
		"a request larger than its limit": func(input *CreateWorkloadInput) {
			input.Containers[0].Resources = &WorkloadResourceRequirements{
				Requests: map[string]string{"memory": "2Gi"},
				Limits:   map[string]string{"memory": "1Gi"},
			}
		},
		"a probe on an init container": func(input *CreateWorkloadInput) {
			input.InitContainers = []WorkloadContainerTemplate{{
				Name: "setup", Image: "example/setup:v1",
				ReadinessProbe: &WorkloadProbe{TCPSocket: &WorkloadTCPSocketAction{Port: "80"}},
			}}
		},
		"a lifecycle hook on an init container": func(input *CreateWorkloadInput) {
			input.InitContainers = []WorkloadContainerTemplate{{
				Name: "setup", Image: "example/setup:v1",
				Lifecycle: &WorkloadLifecycle{
					PreStop: &WorkloadLifecycleHandler{
						Exec: &WorkloadExecAction{Command: []string{"/bin/stop"}},
					},
				},
			}}
		},
		"a description longer than the contract allows": func(input *CreateWorkloadInput) {
			input.Description = strings.Repeat("描", maxWorkloadDescriptionRunes+1)
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := minimalWorkloadInput()
			mutate(&input)
			if err := validateCreateWorkloadInput(input); err == nil {
				t.Fatalf("accepted %s", name)
			}
		})
	}
}

// A name a Deployment carries fine is too long for a Job, whose Pods label
// themselves with it, and longer still than a CronJob may be named.
func TestValidateCreateWorkloadInputBoundsNameLengthByType(t *testing.T) {
	t.Parallel()

	cases := []struct {
		resource WorkloadResource
		length   int
		accepted bool
	}{
		{WorkloadDeployments, maxJobNameLength + 1, true},
		{WorkloadJobs, maxJobNameLength, true},
		{WorkloadJobs, maxJobNameLength + 1, false},
		{WorkloadCronJobs, maxCronJobNameLength, true},
		{WorkloadCronJobs, maxCronJobNameLength + 1, false},
	}

	for _, testCase := range cases {
		input := minimalWorkloadInput()
		input.Resource = testCase.resource
		input.Name = strings.Repeat("a", testCase.length)
		if testCase.resource == WorkloadCronJobs {
			input.Schedule = "*/5 * * * *"
		}
		err := validateCreateWorkloadInput(input)
		if (err == nil) != testCase.accepted {
			t.Fatalf(
				"%s with a %d character name: accepted=%v",
				testCase.resource, testCase.length, err == nil,
			)
		}
	}
}

// The contract states the description in characters, so a Chinese one has to
// reach the same length as an ASCII one.
func TestValidateCreateWorkloadInputCountsDescriptionInCharacters(t *testing.T) {
	t.Parallel()

	input := minimalWorkloadInput()
	input.Description = strings.Repeat("描", maxWorkloadDescriptionRunes)
	if err := validateCreateWorkloadInput(input); err != nil {
		t.Fatalf("rejected a description of %d characters: %v", maxWorkloadDescriptionRunes, err)
	}
}
