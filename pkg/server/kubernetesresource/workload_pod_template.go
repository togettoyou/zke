package kubernetesresource

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	k8svalidation "k8s.io/apimachinery/pkg/util/validation"
)

// The Pod template a typed workload create writes.
//
// Everything here belongs to `spec.template.spec` rather than to the controller,
// so it is shared by all five workload types: a Deployment and a CronJob differ
// in how Pods are produced, not in what a Pod is. Fields Kubernetes derives or
// the platform owns — the selector, the restart policy, `zke.io/workload-id` —
// stay out. Affinity, topology spread and documented container ports have
// bounded service types in workload_advanced.go; host ports and the rest of the
// security context remain managed through YAML.

const (
	maxWorkloadEnvVars            = 100
	maxWorkloadVolumes            = 50
	maxWorkloadVolumeMounts       = 50
	maxWorkloadTolerations        = 50
	maxWorkloadImagePullSecrets   = 20
	maxWorkloadCommandEntries     = 100
	maxWorkloadNodeSelectors      = 50
	maxWorkloadPathBytes          = 4096
	maxWorkloadEnvValueBytes      = 32 * 1024
	maxWorkloadDescriptionRunes   = 1000
	workloadDescriptionAnnotation = "zke.io/description"
)

// A single environment variable.
//
// A literal value and a reference are alternatives, never both: Kubernetes
// rejects `value` together with `valueFrom`, and a form that accepted both would
// have to decide which one wins.
type WorkloadEnvVar struct {
	Name            string                `json:"name"`
	Value           string                `json:"value,omitempty"`
	ConfigMapKeyRef *WorkloadObjectKeyRef `json:"config_map_key_ref,omitempty"`
	SecretKeyRef    *WorkloadObjectKeyRef `json:"secret_key_ref,omitempty"`
}

type WorkloadObjectKeyRef struct {
	Name     string `json:"name"`
	Key      string `json:"key"`
	Optional *bool  `json:"optional,omitempty"`
}

// Compute the container asks for and may use.
//
// Kept as quantity maps rather than four named fields so extended resources —
// `nvidia.com/gpu` and every other device plugin — need no new field and no new
// release. The Console renders CPU and memory as their own inputs and puts GPUs
// in the same map.
type WorkloadResourceRequirements struct {
	Requests map[string]string `json:"requests,omitempty"`
	Limits   map[string]string `json:"limits,omitempty"`
}

type WorkloadVolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mount_path"`
	SubPath   string `json:"sub_path,omitempty"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type WorkloadExecAction struct {
	Command []string `json:"command"`
}

// A port as Kubernetes accepts it: a number, or the name of a container port.
type WorkloadHTTPGetAction struct {
	Path   string `json:"path,omitempty"`
	Port   string `json:"port"`
	Host   string `json:"host,omitempty"`
	Scheme string `json:"scheme,omitempty"`
}

type WorkloadTCPSocketAction struct {
	Port string `json:"port"`
}

type WorkloadProbe struct {
	Exec                *WorkloadExecAction      `json:"exec,omitempty"`
	HTTPGet             *WorkloadHTTPGetAction   `json:"http_get,omitempty"`
	TCPSocket           *WorkloadTCPSocketAction `json:"tcp_socket,omitempty"`
	InitialDelaySeconds *int32                   `json:"initial_delay_seconds,omitempty"`
	PeriodSeconds       *int32                   `json:"period_seconds,omitempty"`
	TimeoutSeconds      *int32                   `json:"timeout_seconds,omitempty"`
	SuccessThreshold    *int32                   `json:"success_threshold,omitempty"`
	FailureThreshold    *int32                   `json:"failure_threshold,omitempty"`
}

type WorkloadLifecycleHandler struct {
	Exec    *WorkloadExecAction    `json:"exec,omitempty"`
	HTTPGet *WorkloadHTTPGetAction `json:"http_get,omitempty"`
}

type WorkloadLifecycle struct {
	PostStart *WorkloadLifecycleHandler `json:"post_start,omitempty"`
	PreStop   *WorkloadLifecycleHandler `json:"pre_stop,omitempty"`
}

type WorkloadEmptyDirVolume struct {
	Medium    string `json:"medium,omitempty"`
	SizeLimit string `json:"size_limit,omitempty"`
}

type WorkloadHostPathVolume struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
}

type WorkloadConfigMapVolume struct {
	Name        string `json:"name"`
	DefaultMode *int32 `json:"default_mode,omitempty"`
	Optional    *bool  `json:"optional,omitempty"`
}

// A Secret mounted as a volume.
//
// Only the reference travels: ZKE neither reads nor returns Secret contents, and
// this names one for the kubelet to mount exactly as an Ingress names one for
// TLS.
type WorkloadSecretVolume struct {
	SecretName  string `json:"secret_name"`
	DefaultMode *int32 `json:"default_mode,omitempty"`
	Optional    *bool  `json:"optional,omitempty"`
}

type WorkloadPersistentVolumeClaimVolume struct {
	ClaimName string `json:"claim_name"`
	ReadOnly  bool   `json:"read_only,omitempty"`
}

type WorkloadNFSVolume struct {
	Server   string `json:"server"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

// Exactly one source is set; the Console picks it and the Server enforces it.
type WorkloadVolume struct {
	Name                  string                               `json:"name"`
	EmptyDir              *WorkloadEmptyDirVolume              `json:"empty_dir,omitempty"`
	HostPath              *WorkloadHostPathVolume              `json:"host_path,omitempty"`
	ConfigMap             *WorkloadConfigMapVolume             `json:"config_map,omitempty"`
	Secret                *WorkloadSecretVolume                `json:"secret,omitempty"`
	PersistentVolumeClaim *WorkloadPersistentVolumeClaimVolume `json:"persistent_volume_claim,omitempty"`
	NFS                   *WorkloadNFSVolume                   `json:"nfs,omitempty"`
}

type WorkloadToleration struct {
	Key               string `json:"key,omitempty"`
	Operator          string `json:"operator,omitempty"`
	Value             string `json:"value,omitempty"`
	Effect            string `json:"effect,omitempty"`
	TolerationSeconds *int64 `json:"toleration_seconds,omitempty"`
}

// ---------------------------------------------------------------------------
// Validation
// ---------------------------------------------------------------------------

func validWorkloadContainerTemplate(container WorkloadContainerTemplate) bool {
	return validWorkloadCommand(container.Command) &&
		validWorkloadCommand(container.Args) &&
		validWorkloadPath(container.WorkingDir, false) &&
		validWorkloadEnv(container.Env) &&
		validWorkloadResources(container.Resources) &&
		validWorkloadVolumeMounts(container.VolumeMounts) &&
		validWorkloadContainerPorts(container.Ports) &&
		validWorkloadProbe(container.LivenessProbe) &&
		validWorkloadProbe(container.ReadinessProbe) &&
		validWorkloadLifecycle(container.Lifecycle)
}

func validWorkloadCommand(values []string) bool {
	if len(values) > maxWorkloadCommandEntries {
		return false
	}
	for _, value := range values {
		// An empty argument is a legal argv entry, but an entry carrying a NUL
		// cannot survive exec and is a sign the value was assembled wrongly.
		if len(value) > maxWorkloadEnvValueBytes || strings.ContainsRune(value, 0) {
			return false
		}
	}
	return true
}

func validWorkloadPath(value string, required bool) bool {
	if value == "" {
		return !required
	}
	return len(value) <= maxWorkloadPathBytes &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func validWorkloadEnv(values []WorkloadEnvVar) bool {
	if len(values) > maxWorkloadEnvVars {
		return false
	}
	names := make(map[string]struct{}, len(values))
	for _, variable := range values {
		if len(k8svalidation.IsEnvVarName(variable.Name)) != 0 ||
			len(variable.Value) > maxWorkloadEnvValueBytes {
			return false
		}
		if _, exists := names[variable.Name]; exists {
			return false
		}
		names[variable.Name] = struct{}{}
		sources := 0
		if variable.Value != "" {
			sources++
		}
		if variable.ConfigMapKeyRef != nil {
			sources++
			if !validWorkloadObjectKeyRef(*variable.ConfigMapKeyRef) {
				return false
			}
		}
		if variable.SecretKeyRef != nil {
			sources++
			if !validWorkloadObjectKeyRef(*variable.SecretKeyRef) {
				return false
			}
		}
		if sources > 1 {
			return false
		}
	}
	return true
}

func validWorkloadObjectKeyRef(reference WorkloadObjectKeyRef) bool {
	return len(k8svalidation.IsDNS1123Subdomain(reference.Name)) == 0 &&
		len(k8svalidation.IsConfigMapKey(reference.Key)) == 0
}

func validWorkloadResources(requirements *WorkloadResourceRequirements) bool {
	if requirements == nil {
		return true
	}
	// The same parser the policy quotas use: qualified names, Kubernetes
	// quantities, nothing negative.
	requests, err := policyResourceList(requirements.Requests)
	if err != nil {
		return false
	}
	limits, err := policyResourceList(requirements.Limits)
	if err != nil {
		return false
	}
	// A container that asks for more than it may use never schedules, and the
	// message it fails with names the resource rather than the field.
	for name, request := range requests {
		if limit, exists := limits[name]; exists && request.Cmp(limit) > 0 {
			return false
		}
	}
	return true
}

func validWorkloadVolumeMounts(mounts []WorkloadVolumeMount) bool {
	if len(mounts) > maxWorkloadVolumeMounts {
		return false
	}
	paths := make(map[string]struct{}, len(mounts))
	for _, mount := range mounts {
		if len(k8svalidation.IsDNS1123Label(mount.Name)) != 0 ||
			!validWorkloadPath(mount.MountPath, true) ||
			// The kubelet mounts at an absolute path, and Kubernetes reads a
			// colon inside one as a separator.
			!strings.HasPrefix(mount.MountPath, "/") ||
			strings.Contains(mount.MountPath, ":") ||
			!validWorkloadPath(mount.SubPath, false) ||
			!validWorkloadSubPath(mount.SubPath) {
			return false
		}
		// Two mounts at one path is not a configuration Kubernetes resolves; one
		// of them silently wins.
		if _, exists := paths[mount.MountPath]; exists {
			return false
		}
		paths[mount.MountPath] = struct{}{}
	}
	return true
}

// A subPath selects inside the volume it is mounted from.
//
// An absolute path or a `..` segment leaves that volume, which is both a
// traversal the platform has no reason to pass on and something Kubernetes
// rejects at admission anyway.
func validWorkloadSubPath(value string) bool {
	return !strings.HasPrefix(value, "/") &&
		!slices.Contains(strings.Split(value, "/"), "..")
}

func validWorkloadProbe(probe *WorkloadProbe) bool {
	if probe == nil {
		return true
	}
	handlers := 0
	if probe.Exec != nil {
		handlers++
		if !validWorkloadExecAction(*probe.Exec) {
			return false
		}
	}
	if probe.HTTPGet != nil {
		handlers++
		if !validWorkloadHTTPGetAction(*probe.HTTPGet) {
			return false
		}
	}
	if probe.TCPSocket != nil {
		handlers++
		if !validWorkloadPort(probe.TCPSocket.Port) {
			return false
		}
	}
	// A probe with no handler never runs, and one with two has no defined
	// meaning; both would be accepted here and rejected by the API server.
	if handlers != 1 {
		return false
	}
	return validNonNegativeInt32(probe.InitialDelaySeconds) &&
		validPositiveInt32(probe.PeriodSeconds) &&
		validPositiveInt32(probe.TimeoutSeconds) &&
		validPositiveInt32(probe.SuccessThreshold) &&
		validPositiveInt32(probe.FailureThreshold)
}

func validWorkloadLifecycle(lifecycle *WorkloadLifecycle) bool {
	if lifecycle == nil {
		return true
	}
	return validWorkloadLifecycleHandler(lifecycle.PostStart) &&
		validWorkloadLifecycleHandler(lifecycle.PreStop)
}

func validWorkloadLifecycleHandler(handler *WorkloadLifecycleHandler) bool {
	if handler == nil {
		return true
	}
	handlers := 0
	if handler.Exec != nil {
		handlers++
		if !validWorkloadExecAction(*handler.Exec) {
			return false
		}
	}
	if handler.HTTPGet != nil {
		handlers++
		if !validWorkloadHTTPGetAction(*handler.HTTPGet) {
			return false
		}
	}
	return handlers == 1
}

func validWorkloadExecAction(action WorkloadExecAction) bool {
	return len(action.Command) > 0 && validWorkloadCommand(action.Command)
}

func validWorkloadHTTPGetAction(action WorkloadHTTPGetAction) bool {
	switch corev1.URIScheme(action.Scheme) {
	case "", corev1.URISchemeHTTP, corev1.URISchemeHTTPS:
	default:
		return false
	}
	return validWorkloadPort(action.Port) &&
		validWorkloadPath(action.Path, false) &&
		validWorkloadPath(action.Host, false)
}

// A port number in range, or the name of a container port.
func validWorkloadPort(value string) bool {
	if value == "" {
		return false
	}
	if number, err := strconv.Atoi(value); err == nil {
		return number > 0 && number <= 65535
	}
	return len(k8svalidation.IsValidPortName(value)) == 0
}

func validWorkloadVolumes(volumes []WorkloadVolume, mountedNames map[string]struct{}) bool {
	if len(volumes) > maxWorkloadVolumes {
		return false
	}
	names := make(map[string]struct{}, len(volumes))
	for _, volume := range volumes {
		if len(k8svalidation.IsDNS1123Label(volume.Name)) != 0 {
			return false
		}
		if _, exists := names[volume.Name]; exists {
			return false
		}
		names[volume.Name] = struct{}{}
		if !validWorkloadVolumeSource(volume) {
			return false
		}
	}
	// A mount naming a volume that does not exist leaves the container unable to
	// start, and the message it fails with names the volume rather than the form
	// field that produced it.
	for name := range mountedNames {
		if _, exists := names[name]; !exists {
			return false
		}
	}
	return true
}

func validWorkloadVolumeSource(volume WorkloadVolume) bool {
	sources := 0
	if volume.EmptyDir != nil {
		sources++
		switch corev1.StorageMedium(volume.EmptyDir.Medium) {
		case "", corev1.StorageMediumMemory:
		default:
			return false
		}
		if volume.EmptyDir.SizeLimit != "" &&
			!validWorkloadQuantity(volume.EmptyDir.SizeLimit) {
			return false
		}
	}
	if volume.HostPath != nil {
		sources++
		if !validWorkloadPath(volume.HostPath.Path, true) ||
			!strings.HasPrefix(volume.HostPath.Path, "/") ||
			!validWorkloadHostPathType(volume.HostPath.Type) {
			return false
		}
	}
	if volume.ConfigMap != nil {
		sources++
		if len(k8svalidation.IsDNS1123Subdomain(volume.ConfigMap.Name)) != 0 ||
			!validWorkloadFileMode(volume.ConfigMap.DefaultMode) {
			return false
		}
	}
	if volume.Secret != nil {
		sources++
		if len(k8svalidation.IsDNS1123Subdomain(volume.Secret.SecretName)) != 0 ||
			!validWorkloadFileMode(volume.Secret.DefaultMode) {
			return false
		}
	}
	if volume.PersistentVolumeClaim != nil {
		sources++
		if len(k8svalidation.IsDNS1123Subdomain(volume.PersistentVolumeClaim.ClaimName)) != 0 {
			return false
		}
	}
	if volume.NFS != nil {
		sources++
		if volume.NFS.Server == "" ||
			len(volume.NFS.Server) > maxWorkloadPathBytes ||
			strings.TrimSpace(volume.NFS.Server) != volume.NFS.Server ||
			!validWorkloadPath(volume.NFS.Path, true) {
			return false
		}
	}
	return sources == 1
}

func validWorkloadHostPathType(value string) bool {
	switch corev1.HostPathType(value) {
	case corev1.HostPathUnset, corev1.HostPathDirectoryOrCreate, corev1.HostPathDirectory,
		corev1.HostPathFileOrCreate, corev1.HostPathFile, corev1.HostPathSocket,
		corev1.HostPathCharDev, corev1.HostPathBlockDev:
		return true
	default:
		return false
	}
}

func validWorkloadFileMode(mode *int32) bool {
	return mode == nil || (*mode >= 0 && *mode <= 0777)
}

func validWorkloadQuantity(value string) bool {
	_, err := policyResourceList(map[string]string{"storage": value})
	return err == nil
}

func validWorkloadTolerations(tolerations []WorkloadToleration) bool {
	if len(tolerations) > maxWorkloadTolerations {
		return false
	}
	for _, toleration := range tolerations {
		if toleration.Key != "" && len(k8svalidation.IsQualifiedName(toleration.Key)) != 0 {
			return false
		}
		switch corev1.TolerationOperator(toleration.Operator) {
		case "", corev1.TolerationOpEqual:
			if len(toleration.Value) != 0 &&
				len(k8svalidation.IsValidLabelValue(toleration.Value)) != 0 {
				return false
			}
		case corev1.TolerationOpExists:
			// `Exists` matches any value, so carrying one means the form and the
			// effect disagree about what is being tolerated.
			if toleration.Value != "" {
				return false
			}
		default:
			return false
		}
		switch corev1.TaintEffect(toleration.Effect) {
		case "", corev1.TaintEffectNoSchedule, corev1.TaintEffectPreferNoSchedule,
			corev1.TaintEffectNoExecute:
		default:
			return false
		}
		// Only NoExecute evicts, so only it has a period to wait before doing so.
		if toleration.TolerationSeconds != nil &&
			corev1.TaintEffect(toleration.Effect) != corev1.TaintEffectNoExecute {
			return false
		}
		if !validNonNegativeInt64(toleration.TolerationSeconds) {
			return false
		}
		// An empty key only means "every taint" together with `Exists`.
		if toleration.Key == "" &&
			corev1.TolerationOperator(toleration.Operator) != corev1.TolerationOpExists {
			return false
		}
	}
	return true
}

func validWorkloadImagePullSecrets(names []string) bool {
	if len(names) > maxWorkloadImagePullSecrets {
		return false
	}
	for _, name := range names {
		if len(k8svalidation.IsDNS1123Subdomain(name)) != 0 {
			return false
		}
	}
	return true
}

func validWorkloadNodeSelector(selector map[string]string) bool {
	if len(selector) > maxWorkloadNodeSelectors {
		return false
	}
	return validNamespaceLabels(selector)
}

func validWorkloadAnnotations(annotations map[string]string) bool {
	for key, value := range annotations {
		if len(k8svalidation.IsQualifiedName(key)) != 0 ||
			len(value) > maxWorkloadEnvValueBytes ||
			strings.ContainsRune(value, 0) {
			return false
		}
	}
	// The description has its own field and its own key; accepting it here as
	// well would give one value two ways in and no rule about which wins.
	_, reserved := annotations[workloadDescriptionAnnotation]
	return !reserved
}

func validPositiveInt32(value *int32) bool {
	return value == nil || *value > 0
}

// ---------------------------------------------------------------------------
// Conversion
// ---------------------------------------------------------------------------

func workloadContainerSpec(container WorkloadContainerTemplate) corev1.Container {
	result := corev1.Container{
		Name:            container.Name,
		Image:           container.Image,
		ImagePullPolicy: corev1.PullPolicy(container.ImagePullPolicy),
		Command:         slices.Clone(container.Command),
		Args:            slices.Clone(container.Args),
		WorkingDir:      container.WorkingDir,
		Env:             workloadEnvSpec(container.Env),
		VolumeMounts:    workloadVolumeMountSpec(container.VolumeMounts),
		LivenessProbe:   workloadProbeSpec(container.LivenessProbe),
		ReadinessProbe:  workloadProbeSpec(container.ReadinessProbe),
		Lifecycle:       workloadLifecycleSpec(container.Lifecycle),
		Ports:           workloadContainerPortSpec(container.Ports),
	}
	if container.Resources != nil {
		requests, _ := policyResourceList(container.Resources.Requests)
		limits, _ := policyResourceList(container.Resources.Limits)
		result.Resources = corev1.ResourceRequirements{Requests: requests, Limits: limits}
	}
	// Only ever set to true: a false security context is the default expressed
	// twice, and writing it would make an unprivileged container look configured.
	if container.Privileged != nil && *container.Privileged {
		privileged := true
		result.SecurityContext = &corev1.SecurityContext{Privileged: &privileged}
	}
	return result
}

func workloadEnvSpec(values []WorkloadEnvVar) []corev1.EnvVar {
	if len(values) == 0 {
		return nil
	}
	result := make([]corev1.EnvVar, 0, len(values))
	for _, variable := range values {
		converted := corev1.EnvVar{Name: variable.Name, Value: variable.Value}
		switch {
		case variable.ConfigMapKeyRef != nil:
			converted.Value = ""
			converted.ValueFrom = &corev1.EnvVarSource{
				ConfigMapKeyRef: &corev1.ConfigMapKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: variable.ConfigMapKeyRef.Name,
					},
					Key:      variable.ConfigMapKeyRef.Key,
					Optional: copyPointer(variable.ConfigMapKeyRef.Optional),
				},
			}
		case variable.SecretKeyRef != nil:
			converted.Value = ""
			converted.ValueFrom = &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: variable.SecretKeyRef.Name,
					},
					Key:      variable.SecretKeyRef.Key,
					Optional: copyPointer(variable.SecretKeyRef.Optional),
				},
			}
		}
		result = append(result, converted)
	}
	return result
}

func workloadVolumeMountSpec(mounts []WorkloadVolumeMount) []corev1.VolumeMount {
	if len(mounts) == 0 {
		return nil
	}
	result := make([]corev1.VolumeMount, 0, len(mounts))
	for _, mount := range mounts {
		result = append(result, corev1.VolumeMount{
			Name:      mount.Name,
			MountPath: mount.MountPath,
			SubPath:   mount.SubPath,
			ReadOnly:  mount.ReadOnly,
		})
	}
	return result
}

func workloadProbeSpec(probe *WorkloadProbe) *corev1.Probe {
	if probe == nil {
		return nil
	}
	result := &corev1.Probe{
		ProbeHandler:     workloadProbeHandler(probe.Exec, probe.HTTPGet, probe.TCPSocket),
		SuccessThreshold: valueOrZero(probe.SuccessThreshold),
		FailureThreshold: valueOrZero(probe.FailureThreshold),
	}
	result.InitialDelaySeconds = valueOrZero(probe.InitialDelaySeconds)
	result.PeriodSeconds = valueOrZero(probe.PeriodSeconds)
	result.TimeoutSeconds = valueOrZero(probe.TimeoutSeconds)
	return result
}

func workloadProbeHandler(
	exec *WorkloadExecAction,
	httpGet *WorkloadHTTPGetAction,
	tcpSocket *WorkloadTCPSocketAction,
) corev1.ProbeHandler {
	switch {
	case exec != nil:
		return corev1.ProbeHandler{
			Exec: &corev1.ExecAction{Command: slices.Clone(exec.Command)},
		}
	case httpGet != nil:
		return corev1.ProbeHandler{HTTPGet: workloadHTTPGetSpec(*httpGet)}
	case tcpSocket != nil:
		return corev1.ProbeHandler{
			TCPSocket: &corev1.TCPSocketAction{Port: workloadPortValue(tcpSocket.Port)},
		}
	default:
		return corev1.ProbeHandler{}
	}
}

func workloadLifecycleSpec(lifecycle *WorkloadLifecycle) *corev1.Lifecycle {
	if lifecycle == nil ||
		(lifecycle.PostStart == nil && lifecycle.PreStop == nil) {
		return nil
	}
	return &corev1.Lifecycle{
		PostStart: workloadLifecycleHandlerSpec(lifecycle.PostStart),
		PreStop:   workloadLifecycleHandlerSpec(lifecycle.PreStop),
	}
}

func workloadLifecycleHandlerSpec(handler *WorkloadLifecycleHandler) *corev1.LifecycleHandler {
	if handler == nil {
		return nil
	}
	switch {
	case handler.Exec != nil:
		return &corev1.LifecycleHandler{
			Exec: &corev1.ExecAction{Command: slices.Clone(handler.Exec.Command)},
		}
	case handler.HTTPGet != nil:
		return &corev1.LifecycleHandler{HTTPGet: workloadHTTPGetSpec(*handler.HTTPGet)}
	default:
		return nil
	}
}

func workloadHTTPGetSpec(action WorkloadHTTPGetAction) *corev1.HTTPGetAction {
	return &corev1.HTTPGetAction{
		Path:   action.Path,
		Port:   workloadPortValue(action.Port),
		Host:   action.Host,
		Scheme: corev1.URIScheme(action.Scheme),
	}
}

func workloadPortValue(value string) intstr.IntOrString {
	if number, err := strconv.Atoi(value); err == nil {
		return intstr.FromInt32(int32(number))
	}
	return intstr.FromString(value)
}

func workloadVolumeSpec(volumes []WorkloadVolume) []corev1.Volume {
	if len(volumes) == 0 {
		return nil
	}
	result := make([]corev1.Volume, 0, len(volumes))
	for _, volume := range volumes {
		converted := corev1.Volume{Name: volume.Name}
		switch {
		case volume.EmptyDir != nil:
			source := &corev1.EmptyDirVolumeSource{
				Medium: corev1.StorageMedium(volume.EmptyDir.Medium),
			}
			if volume.EmptyDir.SizeLimit != "" {
				list, _ := policyResourceList(map[string]string{"storage": volume.EmptyDir.SizeLimit})
				quantity := list["storage"]
				source.SizeLimit = &quantity
			}
			converted.EmptyDir = source
		case volume.HostPath != nil:
			hostPathType := corev1.HostPathType(volume.HostPath.Type)
			converted.HostPath = &corev1.HostPathVolumeSource{
				Path: volume.HostPath.Path,
				Type: &hostPathType,
			}
		case volume.ConfigMap != nil:
			converted.ConfigMap = &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{Name: volume.ConfigMap.Name},
				DefaultMode:          copyPointer(volume.ConfigMap.DefaultMode),
				Optional:             copyPointer(volume.ConfigMap.Optional),
			}
		case volume.Secret != nil:
			converted.Secret = &corev1.SecretVolumeSource{
				SecretName:  volume.Secret.SecretName,
				DefaultMode: copyPointer(volume.Secret.DefaultMode),
				Optional:    copyPointer(volume.Secret.Optional),
			}
		case volume.PersistentVolumeClaim != nil:
			converted.PersistentVolumeClaim = &corev1.PersistentVolumeClaimVolumeSource{
				ClaimName: volume.PersistentVolumeClaim.ClaimName,
				ReadOnly:  volume.PersistentVolumeClaim.ReadOnly,
			}
		case volume.NFS != nil:
			converted.NFS = &corev1.NFSVolumeSource{
				Server:   volume.NFS.Server,
				Path:     volume.NFS.Path,
				ReadOnly: volume.NFS.ReadOnly,
			}
		}
		result = append(result, converted)
	}
	return result
}

func workloadTolerationSpec(tolerations []WorkloadToleration) []corev1.Toleration {
	if len(tolerations) == 0 {
		return nil
	}
	result := make([]corev1.Toleration, 0, len(tolerations))
	for _, toleration := range tolerations {
		result = append(result, corev1.Toleration{
			Key:               toleration.Key,
			Operator:          corev1.TolerationOperator(toleration.Operator),
			Value:             toleration.Value,
			Effect:            corev1.TaintEffect(toleration.Effect),
			TolerationSeconds: copyPointer(toleration.TolerationSeconds),
		})
	}
	return result
}

func workloadImagePullSecretSpec(names []string) []corev1.LocalObjectReference {
	if len(names) == 0 {
		return nil
	}
	result := make([]corev1.LocalObjectReference, 0, len(names))
	for _, name := range names {
		result = append(result, corev1.LocalObjectReference{Name: name})
	}
	return result
}

// The annotations written onto the workload, description included.
func workloadAnnotations(annotations map[string]string, description string) map[string]string {
	if len(annotations) == 0 && description == "" {
		return nil
	}
	result := maps.Clone(annotations)
	if result == nil {
		result = make(map[string]string, 1)
	}
	if description != "" {
		result[workloadDescriptionAnnotation] = description
	}
	return result
}

// The volume names every container mounts, for the cross-check above.
func workloadMountedVolumeNames(groups ...[]WorkloadContainerTemplate) map[string]struct{} {
	names := make(map[string]struct{})
	for _, group := range groups {
		for _, container := range group {
			for _, mount := range container.VolumeMounts {
				names[mount.Name] = struct{}{}
			}
		}
	}
	return names
}

func valueOrZero(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}
