package kubernetesresource

import (
	"maps"
	"slices"

	corev1 "k8s.io/api/core/v1"
)

// Kubernetes Pod template back into the typed template.
//
// The inverse of what `workload_pod_template.go` writes, and the reason a typed
// update can exist at all: an edit form has to open on the object as it stands,
// and a response carrying only a container's name and image would leave the
// form with nothing to show for the fields it is about to submit — every one of
// them would read as empty and be written back as empty.
//
// Only the modeled fields come back. Everything else on the Pod — affinity,
// topology spread, container ports, the rest of the security context — is
// absent here and preserved by the update rather than round-tripped through
// the form; see `workload_update.go`.

func workloadContainerTemplates(containers []corev1.Container) []WorkloadContainerTemplate {
	result := make([]WorkloadContainerTemplate, 0, len(containers))
	for _, container := range containers {
		result = append(result, workloadContainerTemplate(container))
	}
	return result
}

func workloadContainerTemplate(container corev1.Container) WorkloadContainerTemplate {
	result := WorkloadContainerTemplate{
		Name:            container.Name,
		Image:           container.Image,
		ImagePullPolicy: string(container.ImagePullPolicy),
		Command:         slices.Clone(container.Command),
		Args:            slices.Clone(container.Args),
		WorkingDir:      container.WorkingDir,
		Env:             workloadEnvVarView(container.Env),
		Resources:       workloadResourcesView(container.Resources),
		VolumeMounts:    workloadVolumeMountView(container.VolumeMounts),
		LivenessProbe:   workloadProbeView(container.LivenessProbe),
		ReadinessProbe:  workloadProbeView(container.ReadinessProbe),
		Lifecycle:       workloadLifecycleView(container.Lifecycle),
	}
	// Only reported when true, matching what the create path writes: a false
	// `privileged` is the default said twice, and a form that showed it as
	// configured would write it back that way.
	if container.SecurityContext != nil &&
		container.SecurityContext.Privileged != nil &&
		*container.SecurityContext.Privileged {
		privileged := true
		result.Privileged = &privileged
	}
	return result
}

func workloadEnvVarView(values []corev1.EnvVar) []WorkloadEnvVar {
	if len(values) == 0 {
		return nil
	}
	result := make([]WorkloadEnvVar, 0, len(values))
	for _, variable := range values {
		converted := WorkloadEnvVar{Name: variable.Name, Value: variable.Value}
		// Field refs and resource field refs are not modeled, so they arrive as a
		// name with no source. The form shows them as such rather than dropping
		// the variable, and the update preserves the reference it cannot express.
		if variable.ValueFrom != nil {
			if reference := variable.ValueFrom.ConfigMapKeyRef; reference != nil {
				converted.ConfigMapKeyRef = &WorkloadObjectKeyRef{
					Name: reference.Name, Key: reference.Key,
					Optional: copyPointer(reference.Optional),
				}
			}
			if reference := variable.ValueFrom.SecretKeyRef; reference != nil {
				converted.SecretKeyRef = &WorkloadObjectKeyRef{
					Name: reference.Name, Key: reference.Key,
					Optional: copyPointer(reference.Optional),
				}
			}
		}
		result = append(result, converted)
	}
	return result
}

func workloadResourcesView(requirements corev1.ResourceRequirements) *WorkloadResourceRequirements {
	if len(requirements.Requests) == 0 && len(requirements.Limits) == 0 {
		return nil
	}
	return &WorkloadResourceRequirements{
		Requests: quantityMapView(requirements.Requests),
		Limits:   quantityMapView(requirements.Limits),
	}
}

func quantityMapView(list corev1.ResourceList) map[string]string {
	if len(list) == 0 {
		return nil
	}
	result := make(map[string]string, len(list))
	for name, quantity := range list {
		// The quantity's own string, not a normalised one: `1Gi` submitted back
		// as `1073741824` is the same amount and a different object.
		result[string(name)] = quantity.String()
	}
	return result
}

func workloadVolumeMountView(mounts []corev1.VolumeMount) []WorkloadVolumeMount {
	if len(mounts) == 0 {
		return nil
	}
	result := make([]WorkloadVolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		result = append(result, WorkloadVolumeMount{
			Name:      mount.Name,
			MountPath: mount.MountPath,
			SubPath:   mount.SubPath,
			ReadOnly:  mount.ReadOnly,
		})
	}
	return result
}

func workloadProbeView(probe *corev1.Probe) *WorkloadProbe {
	if probe == nil {
		return nil
	}
	result := &WorkloadProbe{
		InitialDelaySeconds: nonZeroInt32(probe.InitialDelaySeconds),
		PeriodSeconds:       nonZeroInt32(probe.PeriodSeconds),
		TimeoutSeconds:      nonZeroInt32(probe.TimeoutSeconds),
		SuccessThreshold:    nonZeroInt32(probe.SuccessThreshold),
		FailureThreshold:    nonZeroInt32(probe.FailureThreshold),
	}
	if probe.Exec != nil {
		result.Exec = &WorkloadExecAction{Command: slices.Clone(probe.Exec.Command)}
	}
	if probe.HTTPGet != nil {
		result.HTTPGet = workloadHTTPGetView(probe.HTTPGet)
	}
	if probe.TCPSocket != nil {
		result.TCPSocket = &WorkloadTCPSocketAction{Port: probe.TCPSocket.Port.String()}
	}
	return result
}

func workloadLifecycleView(lifecycle *corev1.Lifecycle) *WorkloadLifecycle {
	if lifecycle == nil ||
		(lifecycle.PostStart == nil && lifecycle.PreStop == nil) {
		return nil
	}
	return &WorkloadLifecycle{
		PostStart: workloadLifecycleHandlerView(lifecycle.PostStart),
		PreStop:   workloadLifecycleHandlerView(lifecycle.PreStop),
	}
}

func workloadLifecycleHandlerView(handler *corev1.LifecycleHandler) *WorkloadLifecycleHandler {
	if handler == nil || (handler.Exec == nil && handler.HTTPGet == nil) {
		return nil
	}
	result := &WorkloadLifecycleHandler{}
	if handler.Exec != nil {
		result.Exec = &WorkloadExecAction{Command: slices.Clone(handler.Exec.Command)}
	}
	if handler.HTTPGet != nil {
		result.HTTPGet = workloadHTTPGetView(handler.HTTPGet)
	}
	return result
}

func workloadHTTPGetView(action *corev1.HTTPGetAction) *WorkloadHTTPGetAction {
	return &WorkloadHTTPGetAction{
		Path:   action.Path,
		Port:   action.Port.String(),
		Host:   action.Host,
		Scheme: string(action.Scheme),
	}
}

// Volumes whose source the typed form models.
//
// A Pod may carry sources this form has never heard of — projected, CSI,
// downward API — and those come back as a name with no source. The form shows
// them as unmodeled and the update leaves them exactly as they are; dropping
// them from the response would make a mount point at nothing.
func workloadVolumeView(volumes []corev1.Volume) []WorkloadVolume {
	if len(volumes) == 0 {
		return []WorkloadVolume{}
	}
	result := make([]WorkloadVolume, 0, len(volumes))
	for _, volume := range volumes {
		converted := WorkloadVolume{Name: volume.Name}
		switch {
		case volume.EmptyDir != nil:
			converted.EmptyDir = &WorkloadEmptyDirVolume{Medium: string(volume.EmptyDir.Medium)}
			if volume.EmptyDir.SizeLimit != nil {
				converted.EmptyDir.SizeLimit = volume.EmptyDir.SizeLimit.String()
			}
		case volume.HostPath != nil:
			converted.HostPath = &WorkloadHostPathVolume{Path: volume.HostPath.Path}
			if volume.HostPath.Type != nil {
				converted.HostPath.Type = string(*volume.HostPath.Type)
			}
		case volume.ConfigMap != nil:
			converted.ConfigMap = &WorkloadConfigMapVolume{
				Name:        volume.ConfigMap.Name,
				DefaultMode: copyPointer(volume.ConfigMap.DefaultMode),
				Optional:    copyPointer(volume.ConfigMap.Optional),
			}
		case volume.Secret != nil:
			converted.Secret = &WorkloadSecretVolume{
				SecretName:  volume.Secret.SecretName,
				DefaultMode: copyPointer(volume.Secret.DefaultMode),
				Optional:    copyPointer(volume.Secret.Optional),
			}
		case volume.PersistentVolumeClaim != nil:
			converted.PersistentVolumeClaim = &WorkloadPersistentVolumeClaimVolume{
				ClaimName: volume.PersistentVolumeClaim.ClaimName,
				ReadOnly:  volume.PersistentVolumeClaim.ReadOnly,
			}
		case volume.NFS != nil:
			converted.NFS = &WorkloadNFSVolume{
				Server:   volume.NFS.Server,
				Path:     volume.NFS.Path,
				ReadOnly: volume.NFS.ReadOnly,
			}
		}
		result = append(result, converted)
	}
	return result
}

func workloadTolerationView(tolerations []corev1.Toleration) []WorkloadToleration {
	if len(tolerations) == 0 {
		return []WorkloadToleration{}
	}
	result := make([]WorkloadToleration, 0, len(tolerations))
	for _, toleration := range tolerations {
		result = append(result, WorkloadToleration{
			Key:               toleration.Key,
			Operator:          string(toleration.Operator),
			Value:             toleration.Value,
			Effect:            string(toleration.Effect),
			TolerationSeconds: copyPointer(toleration.TolerationSeconds),
		})
	}
	return result
}

func workloadImagePullSecretView(references []corev1.LocalObjectReference) []string {
	if len(references) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(references))
	for _, reference := range references {
		result = append(result, reference.Name)
	}
	return result
}

func workloadNodeSelectorView(selector map[string]string) map[string]string {
	if len(selector) == 0 {
		return map[string]string{}
	}
	return maps.Clone(selector)
}

// Kubernetes defaults a probe's timings to non-zero values it did not receive,
// so a zero means "not set" and returning it as an explicit 0 would submit a
// probe with, say, `periodSeconds: 0` — which Kubernetes rejects.
func nonZeroInt32(value int32) *int32 {
	if value == 0 {
		return nil
	}
	result := value
	return &result
}
