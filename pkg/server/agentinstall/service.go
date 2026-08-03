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

	"github.com/togettoyou/zke/pkg/server/enrollment"
)

const (
	ConfigMapName        = "zke-agent-config"
	EnrollmentSecretName = "zke-agent-enrollment"
	TrustSecretName      = "zke-agent-trust"
	IdentitySecretName   = "zke-agent-identity"
	ServiceAccountName   = "zke-agent"
	DeploymentName       = "zke-agent"
)

var (
	ErrDisabled      = errors.New("Agent installation is disabled")
	ErrTokenRejected = errors.New("Agent installation token rejected")
)

type Config struct {
	Enabled                      bool
	PublicHTTPURL                string
	PublicQUICAddress            string
	Image                        string
	Namespace                    string
	ImagePullPolicy              corev1.PullPolicy
	ListenerCACertificatePEM     []byte
	RegistrationCACertificatePEM []byte
}

type Service struct {
	enrollment *enrollment.Service
	config     Config
}

type CreateInput struct {
	ProjectID      string
	ClusterName    string
	UserID         string
	RequestID      string
	IdempotencyKey string
	Now            time.Time
}

type CreateResult struct {
	ID             string
	ClusterName    string
	ExpiresAt      time.Time
	ManifestURL    string
	InstallCommand string
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
	if !service.config.Enabled {
		return CreateResult{}, ErrDisabled
	}
	result, err := service.enrollment.Create(ctx, enrollment.CreateInput{
		ProjectID:      input.ProjectID,
		ClusterName:    input.ClusterName,
		UserID:         input.UserID,
		RequestID:      input.RequestID,
		IdempotencyKey: input.IdempotencyKey,
		Now:            input.Now,
	})
	if err != nil {
		return CreateResult{}, err
	}
	manifestURL := strings.TrimRight(service.config.PublicHTTPURL, "/") +
		"/agent-install/v1/manifest"
	command := "curl -fsSL -H " +
		shellQuote("Authorization: Bearer "+result.Token) +
		" " + shellQuote(manifestURL) + " | kubectl apply -f -"
	return CreateResult{
		ID:             result.ID,
		ClusterName:    result.ClusterName,
		ExpiresAt:      result.ExpiresAt,
		ManifestURL:    manifestURL,
		InstallCommand: command,
	}, nil
}

func (service *Service) Manifest(
	ctx context.Context,
	token string,
	now time.Time,
) ([]byte, error) {
	if !service.config.Enabled {
		return nil, ErrDisabled
	}
	active, err := service.enrollment.ResolveManifest(ctx, token, now)
	if errors.Is(err, enrollment.ErrTokenRejected) {
		return nil, ErrTokenRejected
	}
	if err != nil {
		return nil, err
	}
	return renderManifest(service.config, active, token)
}

func renderManifest(
	config Config,
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
				},
				Verbs: []string{"get"},
			},
			{
				APIGroups:     []string{""},
				Resources:     []string{"secrets"},
				ResourceNames: []string{IdentitySecretName},
				Verbs:         []string{"update"},
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
	clusterRole := &rbacv1.ClusterRole{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "rbac.authorization.k8s.io/v1",
			Kind:       "ClusterRole",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:   ServiceAccountName,
			Labels: labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				// `patch` covers marking a Node schedulable or unschedulable;
				// `update` is the full-object YAML management path.
				// Draining is not included: it needs the pods/eviction
				// subresource, which the Resource protocol still rejects.
				Verbs: []string{"get", "list", "update", "patch"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"namespaces"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"pods"},
				Verbs:     []string{"get", "list", "update", "delete"},
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
				// container identity.
				Resources: []string{"pods/exec"},
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
				Verbs: []string{"get", "list", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"services"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
			},
			{
				APIGroups: []string{""},
				// ConfigMaps are managed through their typed endpoint. Secrets stay
				// excluded and will use a separate least-privilege sensitive path.
				Resources: []string{"configmaps"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"persistentvolumes", "persistentvolumeclaims"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"storage.k8s.io"},
				Resources: []string{"storageclasses"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"autoscaling"},
				Resources: []string{"horizontalpodautoscalers"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{""},
				// The two Namespace-level constraints: how much may be consumed,
				// and what a container's limits default to.
				Resources: []string{"resourcequotas", "limitranges"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"policy"},
				Resources: []string{"poddisruptionbudgets"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"scheduling.k8s.io"},
				Resources: []string{"priorityclasses"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				// Read-only, and only to answer which discovered resources come
				// from a CRD — Kubernetes discovery does not carry that fact and
				// the resource browser's custom-resource filter is built on it.
				// Defining or changing CRDs is not a capability the Agent has.
				APIGroups: []string{"apiextensions.k8s.io"},
				Resources: []string{"customresourcedefinitions"},
				Verbs:     []string{"get", "list"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"serviceaccounts"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				// Deliberately omit escalate, bind and impersonate. Kubernetes
				// admission keeps delegated authorization within the Agent's
				// existing permission ceiling.
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "clusterroles", "rolebindings", "clusterrolebindings"},
				Verbs:     []string{"get", "list", "create", "update", "delete"},
			},
			{
				APIGroups: []string{"networking.k8s.io"},
				Resources: []string{"ingresses", "networkpolicies"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
			},
			{
				// Gateway API is optional. Kubernetes accepts this RBAC rule even
				// when the CRD is absent; the Server reports that state separately.
				APIGroups: []string{"gateway.networking.k8s.io"},
				Resources: []string{"gateways"},
				Verbs:     []string{"get", "list", "create", "update", "patch", "delete"},
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
						Image:           config.Image,
						ImagePullPolicy: config.ImagePullPolicy,
						Args:            []string{"--config", "/etc/zke-agent/zke-agent.yaml"},
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
		clusterRole,
		clusterRoleBinding,
		deployment,
	}
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

func renderAgentConfig(config Config) (string, error) {
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
	type agentConfig struct {
		Identity     identityConfig     `yaml:"identity"`
		Registration registrationConfig `yaml:"registration"`
		Connection   connectionConfig   `yaml:"connection"`
		LogLevel     string             `yaml:"log_level"`
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
		LogLevel: "info",
	})
	if err != nil {
		return "", fmt.Errorf("encode Agent configuration: %w", err)
	}
	return string(output), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func pointer[T any](value T) *T {
	return &value
}
