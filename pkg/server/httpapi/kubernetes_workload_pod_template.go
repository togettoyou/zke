package httpapi

import (
	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
)

// Request shapes for the Pod template a typed workload create writes, and the
// mapping into the domain input. They are one-to-one with the domain types: the
// HTTP layer names the fields and the domain layer decides what is valid.

type workloadObjectKeyRefRequest struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Optional *bool  `json:"optional"`
}

type workloadEnvVarRequest struct {
	Name            string                       `json:"name"`
	Value           string                       `json:"value"`
	ConfigMapKeyRef *workloadObjectKeyRefRequest `json:"config_map_key_ref"`
	SecretKeyRef    *workloadObjectKeyRefRequest `json:"secret_key_ref"`
}

type workloadResourceRequirementsRequest struct {
	Requests map[string]string `json:"requests"`
	Limits   map[string]string `json:"limits"`
}

type workloadVolumeMountRequest struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	SubPath   string `json:"sub_path"`
	ReadOnly  bool   `json:"read_only"`
}

type workloadExecActionRequest struct {
	Command []string `json:"command"`
}

type workloadHTTPGetActionRequest struct {
	Path   string `json:"path"`
	Port   string `json:"port"`
	Host   string `json:"host"`
	Scheme string `json:"scheme"`
}

type workloadTCPSocketActionRequest struct {
	Port string `json:"port"`
}

type workloadProbeRequest struct {
	Exec                *workloadExecActionRequest      `json:"exec"`
	HTTPGet             *workloadHTTPGetActionRequest   `json:"http_get"`
	TCPSocket           *workloadTCPSocketActionRequest `json:"tcp_socket"`
	InitialDelaySeconds *int32                          `json:"initial_delay_seconds"`
	PeriodSeconds       *int32                          `json:"period_seconds"`
	TimeoutSeconds      *int32                          `json:"timeout_seconds"`
	SuccessThreshold    *int32                          `json:"success_threshold"`
	FailureThreshold    *int32                          `json:"failure_threshold"`
}

type workloadLifecycleHandlerRequest struct {
	Exec    *workloadExecActionRequest    `json:"exec"`
	HTTPGet *workloadHTTPGetActionRequest `json:"http_get"`
}

type workloadLifecycleRequest struct {
	PostStart *workloadLifecycleHandlerRequest `json:"post_start"`
	PreStop   *workloadLifecycleHandlerRequest `json:"pre_stop"`
}

type workloadEmptyDirVolumeRequest struct {
	Medium    string `json:"medium"`
	SizeLimit string `json:"size_limit"`
}

type workloadHostPathVolumeRequest struct {
	Path string `json:"path"`
	Type string `json:"type"`
}

type workloadConfigMapVolumeRequest struct {
	Name        string `json:"name"`
	DefaultMode *int32 `json:"default_mode"`
	Optional    *bool  `json:"optional"`
}

type workloadSecretVolumeRequest struct {
	SecretName  string `json:"secret_name"`
	DefaultMode *int32 `json:"default_mode"`
	Optional    *bool  `json:"optional"`
}

type workloadPersistentVolumeClaimVolumeRequest struct {
	ClaimName string `json:"claim_name"`
	ReadOnly  bool   `json:"read_only"`
}

type workloadNFSVolumeRequest struct {
	Server   string `json:"server"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only"`
}

type workloadVolumeRequest struct {
	Name                  string                                      `json:"name"`
	EmptyDir              *workloadEmptyDirVolumeRequest              `json:"empty_dir"`
	HostPath              *workloadHostPathVolumeRequest              `json:"host_path"`
	ConfigMap             *workloadConfigMapVolumeRequest             `json:"config_map"`
	Secret                *workloadSecretVolumeRequest                `json:"secret"`
	PersistentVolumeClaim *workloadPersistentVolumeClaimVolumeRequest `json:"persistent_volume_claim"`
	NFS                   *workloadNFSVolumeRequest                   `json:"nfs"`
}

type workloadTolerationRequest struct {
	Key               string `json:"key"`
	Operator          string `json:"operator"`
	Value             string `json:"value"`
	Effect            string `json:"effect"`
	TolerationSeconds *int64 `json:"toleration_seconds"`
}

func workloadEnvVars(input []workloadEnvVarRequest) []kubernetesresource.WorkloadEnvVar {
	if len(input) == 0 {
		return nil
	}
	result := make([]kubernetesresource.WorkloadEnvVar, 0, len(input))
	for _, variable := range input {
		result = append(result, kubernetesresource.WorkloadEnvVar{
			Name:            variable.Name,
			Value:           variable.Value,
			ConfigMapKeyRef: workloadObjectKeyRef(variable.ConfigMapKeyRef),
			SecretKeyRef:    workloadObjectKeyRef(variable.SecretKeyRef),
		})
	}
	return result
}

func workloadObjectKeyRef(
	input *workloadObjectKeyRefRequest,
) *kubernetesresource.WorkloadObjectKeyRef {
	if input == nil {
		return nil
	}
	return &kubernetesresource.WorkloadObjectKeyRef{
		Name: input.Name, Key: input.Key, Optional: input.Optional,
	}
}

func workloadResourceRequirements(
	input *workloadResourceRequirementsRequest,
) *kubernetesresource.WorkloadResourceRequirements {
	if input == nil {
		return nil
	}
	return &kubernetesresource.WorkloadResourceRequirements{
		Requests: input.Requests, Limits: input.Limits,
	}
}

func workloadVolumeMounts(
	input []workloadVolumeMountRequest,
) []kubernetesresource.WorkloadVolumeMount {
	if len(input) == 0 {
		return nil
	}
	result := make([]kubernetesresource.WorkloadVolumeMount, 0, len(input))
	for _, mount := range input {
		result = append(result, kubernetesresource.WorkloadVolumeMount{
			Name:      mount.Name,
			MountPath: mount.MountPath,
			SubPath:   mount.SubPath,
			ReadOnly:  mount.ReadOnly,
		})
	}
	return result
}

func workloadExecAction(
	input *workloadExecActionRequest,
) *kubernetesresource.WorkloadExecAction {
	if input == nil {
		return nil
	}
	return &kubernetesresource.WorkloadExecAction{Command: input.Command}
}

func workloadHTTPGetAction(
	input *workloadHTTPGetActionRequest,
) *kubernetesresource.WorkloadHTTPGetAction {
	if input == nil {
		return nil
	}
	return &kubernetesresource.WorkloadHTTPGetAction{
		Path: input.Path, Port: input.Port, Host: input.Host, Scheme: input.Scheme,
	}
}

func workloadProbe(input *workloadProbeRequest) *kubernetesresource.WorkloadProbe {
	if input == nil {
		return nil
	}
	probe := &kubernetesresource.WorkloadProbe{
		Exec:                workloadExecAction(input.Exec),
		HTTPGet:             workloadHTTPGetAction(input.HTTPGet),
		InitialDelaySeconds: input.InitialDelaySeconds,
		PeriodSeconds:       input.PeriodSeconds,
		TimeoutSeconds:      input.TimeoutSeconds,
		SuccessThreshold:    input.SuccessThreshold,
		FailureThreshold:    input.FailureThreshold,
	}
	if input.TCPSocket != nil {
		probe.TCPSocket = &kubernetesresource.WorkloadTCPSocketAction{Port: input.TCPSocket.Port}
	}
	return probe
}

func workloadLifecycle(input *workloadLifecycleRequest) *kubernetesresource.WorkloadLifecycle {
	if input == nil {
		return nil
	}
	return &kubernetesresource.WorkloadLifecycle{
		PostStart: workloadLifecycleHandler(input.PostStart),
		PreStop:   workloadLifecycleHandler(input.PreStop),
	}
}

func workloadLifecycleHandler(
	input *workloadLifecycleHandlerRequest,
) *kubernetesresource.WorkloadLifecycleHandler {
	if input == nil {
		return nil
	}
	return &kubernetesresource.WorkloadLifecycleHandler{
		Exec:    workloadExecAction(input.Exec),
		HTTPGet: workloadHTTPGetAction(input.HTTPGet),
	}
}

func workloadVolumes(input []workloadVolumeRequest) []kubernetesresource.WorkloadVolume {
	if len(input) == 0 {
		return nil
	}
	result := make([]kubernetesresource.WorkloadVolume, 0, len(input))
	for _, volume := range input {
		converted := kubernetesresource.WorkloadVolume{Name: volume.Name}
		if volume.EmptyDir != nil {
			converted.EmptyDir = &kubernetesresource.WorkloadEmptyDirVolume{
				Medium: volume.EmptyDir.Medium, SizeLimit: volume.EmptyDir.SizeLimit,
			}
		}
		if volume.HostPath != nil {
			converted.HostPath = &kubernetesresource.WorkloadHostPathVolume{
				Path: volume.HostPath.Path, Type: volume.HostPath.Type,
			}
		}
		if volume.ConfigMap != nil {
			converted.ConfigMap = &kubernetesresource.WorkloadConfigMapVolume{
				Name:        volume.ConfigMap.Name,
				DefaultMode: volume.ConfigMap.DefaultMode,
				Optional:    volume.ConfigMap.Optional,
			}
		}
		if volume.Secret != nil {
			converted.Secret = &kubernetesresource.WorkloadSecretVolume{
				SecretName:  volume.Secret.SecretName,
				DefaultMode: volume.Secret.DefaultMode,
				Optional:    volume.Secret.Optional,
			}
		}
		if volume.PersistentVolumeClaim != nil {
			converted.PersistentVolumeClaim = &kubernetesresource.WorkloadPersistentVolumeClaimVolume{
				ClaimName: volume.PersistentVolumeClaim.ClaimName,
				ReadOnly:  volume.PersistentVolumeClaim.ReadOnly,
			}
		}
		if volume.NFS != nil {
			converted.NFS = &kubernetesresource.WorkloadNFSVolume{
				Server: volume.NFS.Server, Path: volume.NFS.Path, ReadOnly: volume.NFS.ReadOnly,
			}
		}
		result = append(result, converted)
	}
	return result
}

func workloadTolerations(
	input []workloadTolerationRequest,
) []kubernetesresource.WorkloadToleration {
	if len(input) == 0 {
		return nil
	}
	result := make([]kubernetesresource.WorkloadToleration, 0, len(input))
	for _, toleration := range input {
		result = append(result, kubernetesresource.WorkloadToleration{
			Key:               toleration.Key,
			Operator:          toleration.Operator,
			Value:             toleration.Value,
			Effect:            toleration.Effect,
			TolerationSeconds: toleration.TolerationSeconds,
		})
	}
	return result
}
