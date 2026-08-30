package agentinstall

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer/json"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/togettoyou/zke/pkg/server/enrollment"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/observability"
	"github.com/togettoyou/zke/pkg/shared/workloadbudget"
)

const (
	ConfigMapName        = "zke-agent-config"
	EnrollmentSecretName = "zke-agent-enrollment"
	TrustSecretName      = "zke-agent-trust"
	IdentitySecretName   = "zke-agent-identity"
	ServiceAccountName   = "zke-agent"
	// BaseClusterRoleName holds the permissions ZKE itself decided the Agent
	// needs. The role the Agent is actually bound to is an aggregate, and this
	// one is its first member.
	BaseClusterRoleName = ServiceAccountName + "-base"
	// AggregationLabel is how a Cluster operator widens what the Agent may do
	// without ZKE handing out a blanket grant it cannot justify.
	//
	// It exists because of Helm. A chart installs whatever kinds it declares,
	// and the base role below is an explicit allow-list of the kinds ZKE's own
	// features address — so a chart that ships a CustomResourceDefinition, or
	// creates an instance of one, is refused by Kubernetes rather than by ZKE.
	// Widening the base role to cover every chart would mean granting the Agent
	// everything, permanently, in every Cluster, whether or not anyone installs
	// a chart.
	//
	// The aggregate is the alternative Kubernetes already has for exactly this
	// question: a Cluster operator creates their own ClusterRole carrying the
	// kinds their charts need, labels it with this, and Kubernetes merges it in.
	// The grant is then theirs, written down in their Cluster, visible to
	// `kubectl get clusterrole -l`, and removable without reinstalling anything.
	AggregationLabel = "zke.io/aggregate-to-agent"
	DeploymentName   = "zke-agent"
)

var (
	ErrTokenRejected = errors.New("Agent installation token rejected")
)

type Config struct {
	ListenerCACertificatePEM []byte
}

type manifestConfig struct {
	PublicHTTPURL     string
	PublicQUICAddress string
	// Workload is the Agent's platform workload settings as the enrollment
	// froze them, so a manifest fetched twice for one token installs the same
	// thing however the platform settings moved in between.
	Workload                     store.WorkloadSettings
	Namespace                    string
	ListenerCACertificatePEM     []byte
	RegistrationCACertificatePEM []byte
}

type Service struct {
	enrollment *enrollment.Service
	config     Config
}

type CreateInput struct {
	ProjectID         string
	ClusterName       string
	UserID            string
	RequestID         string
	IdempotencyKey    string
	Now               time.Time
	EndpointProfileID string
	AgentNamespace    string
}

type CreateResult struct {
	ID                string
	ClusterName       string
	ExpiresAt         time.Time
	ManifestPath      string
	Token             string
	EndpointProfileID string
	AgentNamespace    string
}

func NewService(
	enrollmentService *enrollment.Service,
	config Config,
) *Service {
	return &Service{enrollment: enrollmentService, config: config}
}

func (service *Service) Create(
	ctx context.Context,
	input CreateInput,
) (CreateResult, error) {
	result, err := service.enrollment.Create(ctx, enrollment.CreateInput{
		ProjectID:         input.ProjectID,
		ClusterName:       input.ClusterName,
		UserID:            input.UserID,
		RequestID:         input.RequestID,
		IdempotencyKey:    input.IdempotencyKey,
		Now:               input.Now,
		EndpointProfileID: input.EndpointProfileID,
		AgentNamespace:    input.AgentNamespace,
	})
	if err != nil {
		return CreateResult{}, err
	}
	return CreateResult{
		ID: result.ID, ClusterName: result.ClusterName, ExpiresAt: result.ExpiresAt,
		ManifestPath: "/agent-install/v1/manifest", Token: result.Token,
		EndpointProfileID: result.EndpointProfileID,
		AgentNamespace:    result.AgentNamespace,
	}, nil
}

func (service *Service) Manifest(
	ctx context.Context,
	token string,
	now time.Time,
) ([]byte, error) {
	active, err := service.enrollment.ResolveManifest(ctx, token, now)
	if errors.Is(err, enrollment.ErrTokenRejected) {
		return nil, ErrTokenRejected
	}
	if err != nil {
		return nil, err
	}
	snapshot := active.Snapshot
	return renderManifest(manifestConfig{
		PublicHTTPURL:                snapshot.RegistrationURL,
		PublicQUICAddress:            snapshot.QUICAddress,
		Workload:                     snapshot.AgentWorkload,
		Namespace:                    snapshot.AgentNamespace,
		ListenerCACertificatePEM:     service.config.ListenerCACertificatePEM,
		RegistrationCACertificatePEM: []byte(snapshot.RegistrationCACertificatePEM),
	}, active, token)
}

func renderManifest(
	config manifestConfig,
	active enrollment.ManifestEnrollment,
	token string,
) ([]byte, error) {
	labels := map[string]string{
		"app.kubernetes.io/name":       "zke-agent",
		"app.kubernetes.io/managed-by": "zke-server",
	}
	annotations := map[string]string{
		"zke.io/enrollment-id": active.ID,
		"zke.io/project-id":    active.ProjectID,
		"zke.io/cluster-name":  active.ClusterName,
	}
	agentConfig, err := renderAgentConfig(config)
	if err != nil {
		return nil, err
	}
	// Refused here rather than accepted into a manifest Kubernetes would reject:
	// the operator applies this file in their own Cluster, where a rejection is
	// theirs to debug and says nothing about which platform setting caused it.
	agentResources, err := workloadbudget.Requirements(
		config.Workload.CPURequest,
		config.Workload.MemoryRequest,
		config.Workload.CPULimit,
		config.Workload.MemoryLimit,
	)
	if err != nil {
		return nil, fmt.Errorf("render Agent resource budget: %w", err)
	}
	configHash := sha256.New()
	_, _ = configHash.Write([]byte(agentConfig))
	_, _ = configHash.Write(config.ListenerCACertificatePEM)
	_, _ = configHash.Write(config.RegistrationCACertificatePEM)
	annotations["zke.io/config-hash"] = fmt.Sprintf("%x", configHash.Sum(nil))
	namespace := &corev1.Namespace{
		TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
		ObjectMeta: metav1.ObjectMeta{Name: config.Namespace, Labels: labels},
	}
	enrollmentSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        EnrollmentSecretName,
			Namespace:   config.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Type:       corev1.SecretTypeOpaque,
		StringData: map[string]string{"token": token},
	}
	trustData := map[string][]byte{
		"agent-listener-ca.crt": append([]byte(nil), config.ListenerCACertificatePEM...),
	}
	if len(config.RegistrationCACertificatePEM) != 0 {
		trustData["registration-ca.crt"] =
			append([]byte(nil), config.RegistrationCACertificatePEM...)
	}
	trustSecret := &corev1.Secret{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      TrustSecretName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Type: corev1.SecretTypeOpaque,
		Data: trustData,
	}
	configMap := &corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ConfigMapName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Data: map[string]string{"zke-agent.yaml": agentConfig},
	}
	serviceAccount := &corev1.ServiceAccount{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ServiceAccount"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		AutomountServiceAccountToken: pointer(true),
	}
	role := &rbacv1.Role{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "Role"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				ResourceNames: []string{
					EnrollmentSecretName,
					TrustSecretName,
					IdentitySecretName,
					// Named even when collection is off: the Agent only reads
					// it while ingest is enabled, and naming it here keeps
					// enabling collection from requiring an RBAC change.
					observability.IngestSecretName,
				},
				Verbs: []string{"get"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				ResourceNames: []string{
					IdentitySecretName,
					// The ingest credential is written when the collector is
					// installed and removed when it is uninstalled, both by the
					// Agent itself, so it never leaves the Cluster.
					observability.IngestSecretName,
				},
				Verbs: []string{"update", "delete"},
			},
		},
	}
	roleBinding := &rbacv1.RoleBinding{
		TypeMeta: metav1.TypeMeta{APIVersion: "rbac.authorization.k8s.io/v1", Kind: "RoleBinding"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      ServiceAccountName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      ServiceAccountName,
			Namespace: config.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     ServiceAccountName,
		},
	}
	// The role the Agent is bound to carries no rules of its own. Kubernetes'
	// aggregation controller fills it from every ClusterRole labelled below,
	// starting with the base role that follows.
	aggregateClusterRole := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   ServiceAccountName,
			Labels: labels,
		},
		AggregationRule: &rbacv1.AggregationRule{
			ClusterRoleSelectors: []metav1.LabelSelector{{
				MatchLabels: map[string]string{AggregationLabel: "true"},
			}},
		},
	}
	baseClusterRoleLabels := make(map[string]string, len(labels)+1)
	for key, value := range labels {
		baseClusterRoleLabels[key] = value
	}
	baseClusterRoleLabels[AggregationLabel] = "true"
	clusterRole := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   BaseClusterRoleName,
			Labels: baseClusterRoleLabels,
		},
		// Readable resources include watch because temporary Cluster terminal
		// roles delegate that verb. Kubernetes rejects such role creation unless
		// the Agent already holds every delegated permission.
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"metrics.k8s.io"},
				Resources: []string{"nodes", "pods"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				// `patch` covers marking a Node schedulable or unschedulable;
				// `update` is the full-object YAML management path.
				Verbs: []string{"get", "list", "watch", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				// Logs are isolated in a dedicated protocol and receive only the
				// exact read-only Kubernetes Subresource permission.
				Resources: []string{"pods/log"},
				Verbs:     []string{"get"},
			},
			{
				APIGroups: []string{""},
				// Interactive terminals are isolated in Pod Exec, which fixes the
				// shell selection (bash, then /bin/sh) and validates Pod UID and
				// container identity. WebSocket uses GET and SPDY fallback uses POST.
				Resources: []string{"pods/exec"},
				Verbs:     []string{"get", "create"},
			},
			{
				APIGroups: []string{""},
				// Port Forward has a dedicated Agent Stream and never opens a
				// listener beyond the Agent's loopback interface. WebSocket uses
				// GET and SPDY fallback uses POST.
				Resources: []string{"pods/portforward"},
				Verbs:     []string{"get", "create"},
			},
			{
				APIGroups: []string{""},
				// Eviction is the sole Resource Stream subresource allowlist
				// entry, used by Node Drain and by evicting a single Pod. The
				// Agent also requires the dedicated access bit, policy/v1
				// Eviction identity and a Pod UID precondition.
				Resources: []string{"pods/eviction"},
				Verbs:     []string{"create"},
			},
			{
				APIGroups: []string{""},
				// Events are isolated in Resource Watch and deliberately omitted
				// from the generic Resource discovery/read path.
				Resources: []string{"events"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"apps"},
				Resources: []string{
					"deployments",
					"statefulsets",
					"daemonsets",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				// Read-only, and only so a workload's revision history can be
				// read: a Deployment records its revisions as the ReplicaSets it
				// owns, a StatefulSet and a DaemonSet as ControllerRevisions. A
				// rollback writes the recorded Pod template back onto the
				// workload above, so nothing here needs to create, change or
				// prune a revision — that stays the controllers' job.
				APIGroups: []string{"apps"},
				Resources: []string{"replicasets", "controllerrevisions"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"services", "endpoints"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				// Secrets are reachable only through the Server's dedicated Secret
				// API, which sets a flag this Agent checks before it will act on
				// one at all, and never in the Agent's own namespace — that is
				// where its identity key, its enrollment token and the
				// certificates it trusts the Server by live. This grant is what
				// makes that API possible; it is not what authorizes it.
				Resources: []string{"secrets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumes", "persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"autoscaling"},
				Resources: []string{"horizontalpodautoscalers"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"autoscaling.k8s.io"},
				Resources: []string{"verticalpodautoscalers"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"keda.sh"},
				Resources: []string{"scaledobjects"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				// The two Namespace-level constraints: how much may be consumed,
				// and what a container's limits default to.
				Resources: []string{"resourcequotas", "limitranges"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"policy"},
				Resources: []string{"poddisruptionbudgets"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"scheduling.k8s.io"},
				Resources: []string{"priorityclasses"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				// Read-only, and only to answer which discovered resources come
				// from a CRD — Kubernetes discovery does not carry that fact and
				// the resource browser's custom-resource filter is built on it.
				// Defining or changing CRDs is not a capability the Agent has.
				APIGroups: []string{"apiextensions.k8s.io"},
				Resources: []string{"customresourcedefinitions"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				// Held so the Agent can grant it. Kubernetes refuses to create a
				// ClusterRole carrying a permission its creator does not have,
				// and the metrics collector's role is exactly this rule. The
				// Agent never scrapes a kubelet itself.
				APIGroups: []string{""},
				Resources: []string{"nodes/metrics"},
				Verbs:     []string{"get"},
			},
			{
				// Deliberately omit escalate, bind and impersonate. Kubernetes
				// admission keeps delegated authorization within the Agent's
				// existing permission ceiling.
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "clusterroles", "rolebindings", "clusterrolebindings"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"ingresses", "networkpolicies"},
				Verbs:     []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
			{
				// Gateway API is optional. Kubernetes accepts this RBAC rule even
				// when the CRD is absent; the Server reports that state separately.
				APIGroups: []string{"gateway.networking.k8s.io"},
				Resources: []string{
					"gateways", "httproutes", "grpcroutes", "tlsroutes", "tcproutes", "udproutes",
				},
				Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"},
			},
		},
	}
	clusterRoleBinding := &rbacv1.ClusterRoleBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRoleBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   ServiceAccountName,
			Labels: labels,
		},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      ServiceAccountName,
			Namespace: config.Namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     ServiceAccountName,
		},
	}
	replicas := int32(1)
	terminationGracePeriod := int64(30)
	deployment := &appsv1.Deployment{
		TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"},
		ObjectMeta: metav1.ObjectMeta{
			Name:        DeploymentName,
			Namespace:   config.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Strategy: appsv1.DeploymentStrategy{Type: appsv1.RecreateDeploymentStrategyType},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/name": "zke-agent",
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annotations},
				Spec: corev1.PodSpec{
					ServiceAccountName:            ServiceAccountName,
					AutomountServiceAccountToken:  pointer(true),
					TerminationGracePeriodSeconds: &terminationGracePeriod,
					Containers: []corev1.Container{{
						Name:            "zke-agent",
						Image:           config.Workload.Image,
						ImagePullPolicy: corev1.PullPolicy(config.Workload.ImagePullPolicy),
						Resources:       agentResources,
						Args:            []string{"--config", "/etc/zke-agent/zke-agent.yaml"},
						Ports: []corev1.ContainerPort{{
							Name:          observability.IngestPortName,
							ContainerPort: observability.IngestPort,
							Protocol:      corev1.ProtocolTCP,
						}},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: pointer(false),
							ReadOnlyRootFilesystem:   pointer(true),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "config", MountPath: "/etc/zke-agent", ReadOnly: true},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{Name: ConfigMapName},
								},
							},
						},
					},
				},
			},
		},
	}

	objects := []runtime.Object{
		namespace,
		enrollmentSecret,
		trustSecret,
		configMap,
		serviceAccount,
		role,
		roleBinding,
		aggregateClusterRole,
		clusterRole,
		clusterRoleBinding,
		deployment,
	}
	// A ClusterIP Service, and deliberately nothing else: the endpoint is for a
	// collector running in this Cluster, so it needs no Ingress, no node port
	// and no external name.
	//
	// It is installed unconditionally. Enabling metrics later is then a Server
	// setting plus one action in the Console, rather than something that also
	// requires every Cluster to re-apply its Agent manifest.
	objects = append(objects, &corev1.Service{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.IngestServiceName,
			Namespace: config.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: map[string]string{"app.kubernetes.io/name": "zke-agent"},
			Ports: []corev1.ServicePort{{
				Name:       observability.IngestPortName,
				Port:       observability.IngestPort,
				TargetPort: intstr.FromString(observability.IngestPortName),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	})
	serializer := json.NewSerializerWithOptions(
		json.DefaultMetaFactory,
		nil,
		nil,
		json.SerializerOptions{Yaml: true, Pretty: false, Strict: true},
	)
	var output bytes.Buffer
	for index, object := range objects {
		if index != 0 {
			output.WriteString("---\n")
		}
		if err := serializer.Encode(object, &output); err != nil {
			return nil, fmt.Errorf("encode Agent installation manifest: %w", err)
		}
	}
	return output.Bytes(), nil
}

func renderAgentConfig(config manifestConfig) (string, error) {
	type identityConfig struct {
		Namespace   string `yaml:"namespace"`
		SecretName  string `yaml:"secret_name"`
		RenewBefore string `yaml:"renew_before"`
	}
	type registrationConfig struct {
		ServerURL            string `yaml:"server_url"`
		Timeout              string `yaml:"timeout"`
		RetryInitialInterval string `yaml:"retry_initial_interval"`
		RetryMaxInterval     string `yaml:"retry_max_interval"`
	}
	type connectionConfig struct {
		ServerAddress        string `yaml:"server_address"`
		ConnectTimeout       string `yaml:"connect_timeout"`
		RetryInitialInterval string `yaml:"retry_initial_interval"`
		RetryMaxInterval     string `yaml:"retry_max_interval"`
	}
	type metricsIngestConfig struct {
		Address string `yaml:"address"`
	}
	type agentConfig struct {
		Identity      identityConfig       `yaml:"identity"`
		Registration  registrationConfig   `yaml:"registration"`
		Connection    connectionConfig     `yaml:"connection"`
		MetricsIngest *metricsIngestConfig `yaml:"metrics_ingest,omitempty"`
		LogLevel      string               `yaml:"log_level"`
	}
	metricsIngest := &metricsIngestConfig{
		Address: fmt.Sprintf("0.0.0.0:%d", observability.IngestPort),
	}
	output, err := yaml.Marshal(agentConfig{
		Identity: identityConfig{
			Namespace:   config.Namespace,
			SecretName:  IdentitySecretName,
			RenewBefore: "168h",
		},
		Registration: registrationConfig{
			ServerURL:            strings.TrimRight(config.PublicHTTPURL, "/"),
			Timeout:              "10s",
			RetryInitialInterval: "1s",
			RetryMaxInterval:     "15s",
		},
		Connection: connectionConfig{
			ServerAddress:        config.PublicQUICAddress,
			ConnectTimeout:       "10s",
			RetryInitialInterval: "1s",
			RetryMaxInterval:     "30s",
		},
		MetricsIngest: metricsIngest,
		LogLevel:      "info",
	})
	if err != nil {
		return "", fmt.Errorf("encode Agent configuration: %w", err)
	}
	return string(output), nil
}

func pointer[T any](value T) *T {
	return &value
}
