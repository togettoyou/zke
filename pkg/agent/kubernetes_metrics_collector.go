package agent

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/agentprotocol"
	"github.com/togettoyou/zke/pkg/shared/observability"
	"github.com/togettoyou/zke/pkg/shared/workloadbudget"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	ingestTokenBytes = 32
	// What the collector container asked for before its resources became a
	// platform setting. Kept as the fallback for a Server that names none of
	// them, so an older Server still installs the collector it used to.
	legacyCollectorCPURequest    = "50m"
	legacyCollectorMemoryRequest = "128Mi"
)

// newKubernetesMetricsCollectorHandler installs, removes and reports on the
// in-cluster metrics collector.
//
// The Agent does this rather than the Server writing objects through the
// generic resource path, for the same reason terminal sessions work this way:
// the object set is fixed, so the side that has to live with the result is the
// one that decides its shape. It also keeps the ingest credential inside the
// Cluster — the Agent issues it and the Agent verifies it, and it is never
// carried over the wire or stored by the Server.
// collectorPlacement is what the Agent knows about how a collector can reach
// it: where it runs, and the address it can be reached on from inside the
// Cluster.
type collectorPlacement struct {
	Namespace string
	// AdvertisedURL overrides the in-cluster Service. Empty is the normal case.
	AdvertisedURL string
	// InCluster reports whether this Agent runs as a Pod. An Agent that does
	// not has no Endpoint behind its Service, so the Service address would send
	// the collector nowhere.
	InCluster bool
}

// ingestCredentials is the endpoint's view of the accepted tokens, refreshed as
// soon as an install writes one or an uninstall removes it.
//
// Without this the endpoint only learns about a new credential on its next poll,
// and the collector — which starts pushing seconds after the install — spends up
// to that whole interval being answered 401, then backs off exponentially on top
// of it. From the outside a fresh install simply looks broken, and the collector
// status makes that worse by reporting the credential as ready: it reads the
// Secret straight from the API server, so it is right while the endpoint is
// still working from a stale cache.
type ingestCredentials interface {
	refresh(context.Context) error
}

func newKubernetesMetricsCollectorHandler(
	client kubernetes.Interface,
	placement collectorPlacement,
	credentials ingestCredentials,
) agentprotocol.MetricsCollectorHandler {
	namespace := placement.Namespace
	return func(
		ctx context.Context,
		request *agentv1.MetricsCollectorRequest,
	) (*agentv1.MetricsCollectorResponse, error) {
		if client == nil {
			return collectorFailure(
				agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
				http.StatusServiceUnavailable,
				"KubernetesClientUnavailable",
				"Kubernetes client is unavailable",
			), nil
		}
		// Checked again here rather than trusting the Stream layer: this is the
		// last point before the Agent writes to its own Cluster.
		if err := agentprotocol.ValidateMetricsCollectorRequest(request); err != nil {
			return collectorFailure(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest,
				"CollectorRequestInvalid",
				"metrics collector request is invalid",
			), nil
		}
		switch request.GetAction() {
		case agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_STATUS:
			return collectorStatus(ctx, client, namespace)
		case agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_INSTALL:
			ingestURL, err := placement.ingestURL()
			if err != nil {
				// Refused rather than installed: a collector pointed at an
				// address nothing answers on retries forever and reports
				// nothing, which looks like a working install until somebody
				// reads its logs.
				return collectorFailure(
					agentv1.ResultCode_RESULT_CODE_UNAVAILABLE,
					http.StatusServiceUnavailable,
					"CollectorIngestAddressUnknown",
					"this Agent does not run in the Cluster; set metrics_ingest.advertised_url to an address the Cluster can reach it on",
				), nil
			}
			return installMetricsCollector(
				ctx, client, namespace, ingestURL, request, credentials,
			)
		case agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_UNINSTALL:
			return uninstallMetricsCollector(ctx, client, namespace, credentials)
		default:
			return collectorFailure(
				agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
				http.StatusBadRequest,
				"CollectorActionUnsupported",
				"metrics collector action is not supported",
			), nil
		}
	}
}

func collectorStatus(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
) (*agentv1.MetricsCollectorResponse, error) {
	state := &agentv1.MetricsCollectorState{Namespace: namespace}
	deployment, err := client.AppsV1().Deployments(namespace).Get(
		ctx,
		observability.CollectorName,
		metav1.GetOptions{},
	)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return collectorKubernetesFailure("read metrics collector Deployment", err), nil
	default:
		// An object with our name that we do not own is reported as
		// "not installed by ZKE": claiming it would let an install overwrite
		// something the Cluster's own operators put there.
		if managedByAgent(deployment.Labels) {
			state.Installed = true
			state.Image = collectorImageOf(deployment)
			if deployment.Spec.Replicas != nil {
				state.DesiredReplicas = *deployment.Spec.Replicas
			}
			state.ReadyReplicas = min(
				deployment.Status.ReadyReplicas,
				state.DesiredReplicas,
			)
		}
	}
	secret, err := client.CoreV1().Secrets(namespace).Get(
		ctx,
		observability.IngestSecretName,
		metav1.GetOptions{},
	)
	if err != nil && !apierrors.IsNotFound(err) {
		return collectorKubernetesFailure("read metrics ingest Secret", err), nil
	}
	state.CredentialReady = err == nil &&
		len(secret.Data[observability.IngestTokenKey]) >= minMetricsIngestTokenLength

	components, failure := componentStates(ctx, client, namespace, state)
	if failure != nil {
		return failure, nil
	}
	state.Components = components
	return &agentv1.MetricsCollectorResponse{
		Result: agentv1.ResultCode_RESULT_CODE_OK,
		State:  state,
	}, nil
}

// componentStates reports each installed workload separately.
//
// One aggregate "installed" would hide the case this bundle exists to make
// visible: a Cluster running the collector and the object exporter but not the
// node exporter answers half the query catalogue and nothing about disk or
// network, and an operator reading a single green badge would have no idea why.
func componentStates(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	collector *agentv1.MetricsCollectorState,
) ([]*agentv1.MetricsComponentState, *agentv1.MetricsCollectorResponse) {
	components := []*agentv1.MetricsComponentState{{
		Component:       observability.ComponentCollector,
		Installed:       collector.GetInstalled(),
		Image:           collector.GetImage(),
		DesiredReplicas: collector.GetDesiredReplicas(),
		ReadyReplicas:   collector.GetReadyReplicas(),
	}}

	kubeState := &agentv1.MetricsComponentState{Component: observability.ComponentKubeState}
	deployment, err := client.AppsV1().Deployments(namespace).Get(
		ctx, observability.KubeStateName, metav1.GetOptions{},
	)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, collectorKubernetesFailure("read kube-state-metrics Deployment", err)
	default:
		if managedByAgent(deployment.Labels) {
			kubeState.Installed = true
			kubeState.Image = containerImageOf(
				deployment.Spec.Template.Spec.Containers,
				observability.KubeStateContainerName,
			)
			if deployment.Spec.Replicas != nil {
				kubeState.DesiredReplicas = *deployment.Spec.Replicas
			}
			kubeState.ReadyReplicas = min(
				deployment.Status.ReadyReplicas,
				kubeState.DesiredReplicas,
			)
		}
	}
	components = append(components, kubeState)

	nodeExporter := &agentv1.MetricsComponentState{Component: observability.ComponentNodeExporter}
	daemonSet, err := client.AppsV1().DaemonSets(namespace).Get(
		ctx, observability.NodeExporterName, metav1.GetOptions{},
	)
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, collectorKubernetesFailure("read node-exporter DaemonSet", err)
	default:
		if managedByAgent(daemonSet.Labels) {
			nodeExporter.Installed = true
			nodeExporter.Image = containerImageOf(
				daemonSet.Spec.Template.Spec.Containers,
				observability.NodeExporterContainerName,
			)
			nodeExporter.DesiredReplicas = daemonSet.Status.DesiredNumberScheduled
			nodeExporter.ReadyReplicas = min(
				daemonSet.Status.NumberReady,
				nodeExporter.DesiredReplicas,
			)
		}
	}
	if !nodeExporter.GetInstalled() {
		// The reason the last install recorded, if there was one. It only
		// survives on the collector's own ConfigMap, so a Cluster with no
		// collector reports no reason either — which is correct, nobody has
		// tried to install anything there.
		configMap, err := client.CoreV1().ConfigMaps(namespace).Get(
			ctx, observability.CollectorConfigMapName, metav1.GetOptions{},
		)
		if err == nil && managedByAgent(configMap.Labels) {
			nodeExporter.UnavailableReason = configMap.Annotations[nodeExporterReasonAnnotation]
			nodeExporter.UnavailableMessage = configMap.Annotations[nodeExporterMessageAnnotation]
		}
	}
	components = append(components, nodeExporter)
	return components, nil
}

func containerImageOf(containers []corev1.Container, name string) string {
	for _, container := range containers {
		if container.Name == name {
			return container.Image
		}
	}
	return ""
}

// ingestURL reports where the collector should send batches, or an error when
// this Agent cannot say.
func (placement collectorPlacement) ingestURL() (string, error) {
	if advertised := strings.TrimSpace(placement.AdvertisedURL); advertised != "" {
		return strings.TrimRight(advertised, "/") + observability.IngestWritePath, nil
	}
	if !placement.InCluster {
		return "", errors.New("Agent is not running in the Cluster")
	}
	return fmt.Sprintf(
		"http://%s.%s.svc:%d%s",
		observability.IngestServiceName,
		placement.Namespace,
		observability.IngestPort,
		observability.IngestWritePath,
	), nil
}

func installMetricsCollector(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	ingestURL string,
	request *agentv1.MetricsCollectorRequest,
	credentials ingestCredentials,
) (*agentv1.MetricsCollectorResponse, error) {
	if err := ensureIngestSecret(ctx, client, namespace); err != nil {
		return collectorKubernetesFailure("ensure metrics ingest Secret", err), nil
	}
	// Before the collector exists, so it is never answered 401 by an endpoint
	// that has not noticed the credential this install just wrote.
	//
	// Refused rather than continued when this fails: the read goes to the API
	// server this install just wrote the Secret through, so a failure here means
	// the rest of the install is about to fail too, and stopping now leaves less
	// behind than a collector pointed at an endpoint that will not accept it.
	if err := refreshIngestCredentials(ctx, credentials); err != nil {
		return collectorKubernetesFailure("refresh metrics ingest credential", err), nil
	}
	labels := observability.CollectorLabels()

	serviceAccount := &corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.CollectorName,
			Namespace: namespace,
			Labels:    labels,
		},
		AutomountServiceAccountToken: pointerTo(true),
	}
	if err := applyCollectorObject(
		ctx,
		"ServiceAccount",
		func() (map[string]string, error) {
			existing, err := client.CoreV1().ServiceAccounts(namespace).Get(
				ctx, serviceAccount.Name, metav1.GetOptions{},
			)
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(namespace).Create(
				ctx, serviceAccount, metav1.CreateOptions{},
			)
			return err
		},
		func() error {
			_, err := client.CoreV1().ServiceAccounts(namespace).Update(
				ctx, serviceAccount, metav1.UpdateOptions{},
			)
			return err
		},
	); err != nil {
		return collectorApplyFailure("ServiceAccount", err), nil
	}

	clusterRole := &rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{Name: observability.CollectorName, Labels: labels},
		Rules: []rbacv1.PolicyRule{
			{
				// Discovery only: which nodes exist and how to reach them.
				APIGroups: []string{""},
				Resources: []string{"nodes"},
				Verbs:     []string{"get", "list", "watch"},
			},
			{
				// The metrics endpoints themselves. Narrower than nodes/proxy,
				// which would open every kubelet path rather than these two.
				APIGroups: []string{""},
				Resources: []string{"nodes/metrics"},
				Verbs:     []string{"get"},
			},
		},
	}
	if err := applyCollectorObject(
		ctx,
		"ClusterRole",
		func() (map[string]string, error) {
			existing, err := client.RbacV1().ClusterRoles().Get(
				ctx, clusterRole.Name, metav1.GetOptions{},
			)
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.RbacV1().ClusterRoles().Create(
				ctx, clusterRole, metav1.CreateOptions{},
			)
			return err
		},
		func() error {
			_, err := client.RbacV1().ClusterRoles().Update(
				ctx, clusterRole, metav1.UpdateOptions{},
			)
			return err
		},
	); err != nil {
		return collectorApplyFailure("ClusterRole", err), nil
	}

	binding := &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: observability.CollectorName, Labels: labels},
		Subjects: []rbacv1.Subject{{
			Kind:      "ServiceAccount",
			Name:      observability.CollectorName,
			Namespace: namespace,
		}},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     observability.CollectorName,
		},
	}
	if err := applyCollectorObject(
		ctx,
		"ClusterRoleBinding",
		func() (map[string]string, error) {
			existing, err := client.RbacV1().ClusterRoleBindings().Get(
				ctx, binding.Name, metav1.GetOptions{},
			)
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.RbacV1().ClusterRoleBindings().Create(
				ctx, binding, metav1.CreateOptions{},
			)
			return err
		},
		func() error {
			// RoleRef is immutable, so a binding that already points elsewhere
			// is replaced rather than updated.
			if err := client.RbacV1().ClusterRoleBindings().Delete(
				ctx, binding.Name, metav1.DeleteOptions{},
			); err != nil && !apierrors.IsNotFound(err) {
				return err
			}
			_, err := client.RbacV1().ClusterRoleBindings().Create(
				ctx, binding, metav1.CreateOptions{},
			)
			return err
		},
	); err != nil {
		return collectorApplyFailure("ClusterRoleBinding", err), nil
	}

	// The targets come before the collector's own configuration, because that
	// configuration says what to scrape and a job for something that was never
	// installed fails every interval.
	if request.GetKubeStateMetrics() != nil {
		if err := applyKubeStateMetrics(
			ctx, client, namespace, request.GetKubeStateMetrics(),
		); err != nil {
			return collectorApplyFailure("kube-state-metrics", err), nil
		}
	}
	nodeExporterReason, nodeExporterMessage := "", ""
	if request.GetNodeExporter() != nil {
		nodeExporterReason, nodeExporterMessage = applyNodeExporter(
			ctx, client, namespace, request.GetNodeExporter(),
		)
	}

	configMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.CollectorConfigMapName,
			Namespace: namespace,
			Labels:    labels,
			// Why the node metrics exporter is missing, recorded where a later
			// status read can still find it. Without this the Console would show
			// "not installed" for a component the Cluster refused, and the
			// operator would have to reinstall just to see the reason again.
			// Annotations rather than data: changing them does not remount the
			// collector's configuration volume.
			Annotations: nodeExporterAnnotations(nodeExporterReason, nodeExporterMessage),
		},
		Data: map[string]string{
			observability.CollectorConfigKey: renderCollectorScrapeConfig(
				namespace,
				request,
				request.GetNodeExporter() != nil && nodeExporterReason == "",
			),
		},
	}
	if err := applyCollectorObject(
		ctx,
		"ConfigMap",
		func() (map[string]string, error) {
			existing, err := client.CoreV1().ConfigMaps(namespace).Get(
				ctx, configMap.Name, metav1.GetOptions{},
			)
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.CoreV1().ConfigMaps(namespace).Create(
				ctx, configMap, metav1.CreateOptions{},
			)
			return err
		},
		func() error {
			_, err := client.CoreV1().ConfigMaps(namespace).Update(
				ctx, configMap, metav1.UpdateOptions{},
			)
			return err
		},
	); err != nil {
		return collectorApplyFailure("ConfigMap", err), nil
	}

	deployment, err := renderCollectorDeployment(namespace, ingestURL, labels, request)
	if err != nil {
		return collectorFailure(
			agentv1.ResultCode_RESULT_CODE_INVALID_ARGUMENT,
			http.StatusBadRequest,
			"CollectorConfigurationInvalid",
			"metrics collector configuration is invalid",
		), nil
	}
	if err := applyCollectorObject(
		ctx,
		"Deployment",
		func() (map[string]string, error) {
			existing, err := client.AppsV1().Deployments(namespace).Get(
				ctx, deployment.Name, metav1.GetOptions{},
			)
			if err != nil {
				return nil, err
			}
			return existing.Labels, nil
		},
		func() error {
			_, err := client.AppsV1().Deployments(namespace).Create(
				ctx, deployment, metav1.CreateOptions{},
			)
			return err
		},
		func() error {
			_, err := client.AppsV1().Deployments(namespace).Update(
				ctx, deployment, metav1.UpdateOptions{},
			)
			return err
		},
	); err != nil {
		return collectorApplyFailure("Deployment", err), nil
	}
	return collectorStatus(ctx, client, namespace)
}

// refreshIngestCredentials tells the endpoint to re-read the Secret now. A nil
// cache means this Agent serves no ingest endpoint, which is the case in tests
// that only exercise the object set.
func refreshIngestCredentials(ctx context.Context, credentials ingestCredentials) error {
	if credentials == nil {
		return nil
	}
	return credentials.refresh(ctx)
}

func uninstallMetricsCollector(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
	credentials ingestCredentials,
) (*agentv1.MetricsCollectorResponse, error) {
	// Reverse of the install order: the workload first, so nothing keeps
	// running against permissions that are about to disappear. The credential
	// goes last and does go: leaving it behind would keep a usable token in the
	// Cluster for a collector that no longer exists.
	//
	// The scrape targets go with it. They were installed as one thing and they
	// are removed as one thing — leaving an exporter behind that nothing scrapes
	// would keep consuming a Node's memory for data nobody reads.
	deletions := []struct {
		kind   string
		labels func() (map[string]string, error)
		delete func() error
	}{
		{
			kind: "node-exporter DaemonSet",
			labels: func() (map[string]string, error) {
				existing, err := client.AppsV1().DaemonSets(namespace).Get(
					ctx, observability.NodeExporterName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.AppsV1().DaemonSets(namespace).Delete(
					ctx, observability.NodeExporterName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "node-exporter ServiceAccount",
			labels: func() (map[string]string, error) {
				existing, err := client.CoreV1().ServiceAccounts(namespace).Get(
					ctx, observability.NodeExporterName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.CoreV1().ServiceAccounts(namespace).Delete(
					ctx, observability.NodeExporterName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "kube-state-metrics Deployment",
			labels: func() (map[string]string, error) {
				existing, err := client.AppsV1().Deployments(namespace).Get(
					ctx, observability.KubeStateName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.AppsV1().Deployments(namespace).Delete(
					ctx, observability.KubeStateName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "kube-state-metrics Service",
			labels: func() (map[string]string, error) {
				existing, err := client.CoreV1().Services(namespace).Get(
					ctx, observability.KubeStateName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.CoreV1().Services(namespace).Delete(
					ctx, observability.KubeStateName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "kube-state-metrics ClusterRoleBinding",
			labels: func() (map[string]string, error) {
				existing, err := client.RbacV1().ClusterRoleBindings().Get(
					ctx, observability.KubeStateName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.RbacV1().ClusterRoleBindings().Delete(
					ctx, observability.KubeStateName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "kube-state-metrics ClusterRole",
			labels: func() (map[string]string, error) {
				existing, err := client.RbacV1().ClusterRoles().Get(
					ctx, observability.KubeStateName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.RbacV1().ClusterRoles().Delete(
					ctx, observability.KubeStateName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "kube-state-metrics ServiceAccount",
			labels: func() (map[string]string, error) {
				existing, err := client.CoreV1().ServiceAccounts(namespace).Get(
					ctx, observability.KubeStateName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.CoreV1().ServiceAccounts(namespace).Delete(
					ctx, observability.KubeStateName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "Deployment",
			labels: func() (map[string]string, error) {
				existing, err := client.AppsV1().Deployments(namespace).Get(
					ctx, observability.CollectorName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.AppsV1().Deployments(namespace).Delete(
					ctx, observability.CollectorName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "ConfigMap",
			labels: func() (map[string]string, error) {
				existing, err := client.CoreV1().ConfigMaps(namespace).Get(
					ctx, observability.CollectorConfigMapName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.CoreV1().ConfigMaps(namespace).Delete(
					ctx, observability.CollectorConfigMapName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "ClusterRoleBinding",
			labels: func() (map[string]string, error) {
				existing, err := client.RbacV1().ClusterRoleBindings().Get(
					ctx, observability.CollectorName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.RbacV1().ClusterRoleBindings().Delete(
					ctx, observability.CollectorName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "ClusterRole",
			labels: func() (map[string]string, error) {
				existing, err := client.RbacV1().ClusterRoles().Get(
					ctx, observability.CollectorName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.RbacV1().ClusterRoles().Delete(
					ctx, observability.CollectorName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "ServiceAccount",
			labels: func() (map[string]string, error) {
				existing, err := client.CoreV1().ServiceAccounts(namespace).Get(
					ctx, observability.CollectorName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.CoreV1().ServiceAccounts(namespace).Delete(
					ctx, observability.CollectorName, metav1.DeleteOptions{},
				)
			},
		},
		{
			kind: "Secret",
			labels: func() (map[string]string, error) {
				existing, err := client.CoreV1().Secrets(namespace).Get(
					ctx, observability.IngestSecretName, metav1.GetOptions{},
				)
				if err != nil {
					return nil, err
				}
				return existing.Labels, nil
			},
			delete: func() error {
				return client.CoreV1().Secrets(namespace).Delete(
					ctx, observability.IngestSecretName, metav1.DeleteOptions{},
				)
			},
		},
	}
	for _, deletion := range deletions {
		labels, err := deletion.labels()
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return collectorKubernetesFailure("read "+deletion.kind, err), nil
		}
		if !managedByAgent(labels) {
			// Someone else's object under our name. Refusing beats deleting
			// something this Agent never created.
			return collectorFailure(
				agentv1.ResultCode_RESULT_CODE_CONFLICT,
				http.StatusConflict,
				"CollectorObjectNotManaged",
				"an existing "+deletion.kind+" with the collector name is not managed by ZKE",
			), nil
		}
		if err := deletion.delete(); err != nil && !apierrors.IsNotFound(err) {
			return collectorKubernetesFailure("delete "+deletion.kind, err), nil
		}
	}
	// The credential was deleted last so that no usable token outlives the
	// collector. That only holds if the endpoint stops accepting it too —
	// otherwise the token is gone from the Cluster while the Agent still honours
	// its cached copy.
	if err := refreshIngestCredentials(ctx, credentials); err != nil {
		return collectorKubernetesFailure("refresh metrics ingest credential", err), nil
	}
	return collectorStatus(ctx, client, namespace)
}

// ensureIngestSecret creates the credential if it is missing and leaves an
// existing one alone. Installing twice must not invalidate the token a running
// collector already mounted.
func ensureIngestSecret(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
) error {
	existing, err := client.CoreV1().Secrets(namespace).Get(
		ctx,
		observability.IngestSecretName,
		metav1.GetOptions{},
	)
	if err == nil {
		if !managedByAgent(existing.Labels) {
			return errors.New("existing metrics ingest Secret is not managed by ZKE")
		}
		if len(existing.Data[observability.IngestTokenKey]) >= minMetricsIngestTokenLength {
			return nil
		}
	} else if !apierrors.IsNotFound(err) {
		return err
	}
	value := make([]byte, ingestTokenBytes)
	if _, err := rand.Read(value); err != nil {
		return errors.New("generate metrics ingest token")
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.IngestSecretName,
			Namespace: namespace,
			Labels:    observability.CollectorLabels(),
		},
		Type: corev1.SecretTypeOpaque,
		// Data rather than StringData: the value is already a byte slice here,
		// and StringData would be converted by the API server, leaving the
		// object this Agent just wrote different from the one it reads back.
		Data: map[string][]byte{
			observability.IngestTokenKey: []byte(
				base64.RawURLEncoding.EncodeToString(value),
			),
		},
	}
	if apierrors.IsNotFound(err) {
		_, createErr := client.CoreV1().Secrets(namespace).Create(
			ctx, secret, metav1.CreateOptions{},
		)
		return createErr
	}
	_, updateErr := client.CoreV1().Secrets(namespace).Update(
		ctx, secret, metav1.UpdateOptions{},
	)
	return updateErr
}

// applyCollectorObject creates the object or updates the one already there,
// refusing any object that carries the collector's name without its ownership
// label.
func applyCollectorObject(
	_ context.Context,
	kind string,
	labels func() (map[string]string, error),
	create func() error,
	update func() error,
) error {
	current, err := labels()
	if apierrors.IsNotFound(err) {
		if createErr := create(); createErr != nil && !apierrors.IsAlreadyExists(createErr) {
			return createErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	if !managedByAgent(current) {
		return fmt.Errorf(
			"existing %s with the collector name is not managed by ZKE",
			kind,
		)
	}
	return update()
}

func renderCollectorDeployment(
	namespace string,
	ingestURL string,
	labels map[string]string,
	request *agentv1.MetricsCollectorRequest,
) (*appsv1.Deployment, error) {
	bufferSize, err := resource.ParseQuantity(request.GetBufferSize())
	if err != nil {
		return nil, err
	}
	resources, err := collectorResources(request)
	if err != nil {
		return nil, err
	}
	replicas := int32(1)
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      observability.CollectorName,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			// Two collectors would scrape and forward the same series twice.
			// Recreate keeps that from happening during a rollout.
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RecreateDeploymentStrategyType,
			},
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{
				"app.kubernetes.io/name": observability.CollectorName,
			}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					ServiceAccountName:           observability.CollectorName,
					AutomountServiceAccountToken: pointerTo(true),
					Containers: []corev1.Container{{
						Name:            observability.CollectorContainerName,
						Image:           request.GetImage(),
						ImagePullPolicy: corev1.PullPolicy(request.GetImagePullPolicy()),
						Args: []string{
							"-promscrape.config=/etc/zke-metrics/" + observability.CollectorConfigKey,
							"-remoteWrite.url=" + ingestURL,
							"-remoteWrite.bearerTokenFile=/etc/zke-metrics-credentials/" +
								observability.IngestTokenKey,
							"-remoteWrite.tmpDataPath=/buffer",
							// Plain bytes rather than the configured string: the
							// buffer size is a Kubernetes quantity because it is
							// also the volume's size limit, and vmagent does not
							// parse that spelling — it rejected "512Mi" outright.
							// Converting here keeps one value in configuration.
							"-remoteWrite.maxDiskUsagePerURL=" +
								strconv.FormatInt(bufferSize.Value(), 10),
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: pointerTo(false),
							ReadOnlyRootFilesystem:   pointerTo(true),
							Capabilities: &corev1.Capabilities{
								Drop: []corev1.Capability{"ALL"},
							},
						},
						Resources: resources,
						VolumeMounts: []corev1.VolumeMount{
							{Name: "config", MountPath: "/etc/zke-metrics", ReadOnly: true},
							{
								Name:      "credentials",
								MountPath: "/etc/zke-metrics-credentials",
								ReadOnly:  true,
							},
							{Name: "buffer", MountPath: "/buffer"},
						},
					}},
					Volumes: []corev1.Volume{
						{
							Name: "config",
							VolumeSource: corev1.VolumeSource{
								ConfigMap: &corev1.ConfigMapVolumeSource{
									LocalObjectReference: corev1.LocalObjectReference{
										Name: observability.CollectorConfigMapName,
									},
								},
							},
						},
						{
							Name: "credentials",
							VolumeSource: corev1.VolumeSource{
								Secret: &corev1.SecretVolumeSource{
									SecretName: observability.IngestSecretName,
								},
							},
						},
						{
							// An emptyDir, not a PersistentVolume: the buffer
							// covers a Server or Agent outage, not the loss of
							// the collector Pod. Requiring a volume would make
							// collection depend on dynamic provisioning.
							Name: "buffer",
							VolumeSource: corev1.VolumeSource{
								EmptyDir: &corev1.EmptyDirVolumeSource{
									SizeLimit: &bufferSize,
								},
							},
						},
					},
				},
			},
		},
	}, nil
}

// collectorResources turns the Server's quantities into the container's
// resource block.
//
// An empty entry is dropped rather than parsed: Kubernetes has no spelling for
// "no limit" other than the absence of the key. When the Server names none of
// the four the legacy requests are used instead — that is what a Server too old
// to know these fields is asking for, and it must not silently become an
// unbounded collector in somebody's Cluster.
func collectorResources(
	request *agentv1.MetricsCollectorRequest,
) (corev1.ResourceRequirements, error) {
	if request.GetCpuRequest() == "" && request.GetMemoryRequest() == "" &&
		request.GetCpuLimit() == "" && request.GetMemoryLimit() == "" {
		return corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse(legacyCollectorCPURequest),
				corev1.ResourceMemory: resource.MustParse(legacyCollectorMemoryRequest),
			},
		}, nil
	}
	return workloadbudget.Requirements(
		request.GetCpuRequest(),
		request.GetMemoryRequest(),
		request.GetCpuLimit(),
		request.GetMemoryLimit(),
	)
}

// componentResources does the same for one of the additional scrape targets.
// There is no legacy fallback here: these components did not exist before the
// Server learned to name their budgets, so an empty set of four means the
// operator cleared them on purpose.
func componentResources(
	component *agentv1.MetricsCollectorComponent,
) (corev1.ResourceRequirements, error) {
	return workloadbudget.Requirements(
		component.GetCpuRequest(),
		component.GetMemoryRequest(),
		component.GetCpuLimit(),
		component.GetMemoryLimit(),
	)
}

// renderCollectorScrapeConfig covers exactly the targets this install puts into
// the Cluster: the kubelet always, and the two additional exporters when the
// Server asked for them.
//
// A job is written only for a target that exists. A scrape configuration naming
// something that was never installed produces a job that fails every interval,
// which reads as a broken Cluster rather than as a target nobody asked for.
func renderCollectorScrapeConfig(
	namespace string,
	request *agentv1.MetricsCollectorRequest,
	nodeExporterInstalled bool,
) string {
	config := fmt.Sprintf(`global:
  scrape_interval: %s
scrape_configs:
`, request.GetScrapeInterval())
	for _, job := range kubeletScrapeJobs {
		config += renderKubeletScrapeJob(job, request.GetKubeletMetricsPort())
	}
	if request.GetKubeStateMetrics() != nil {
		config += renderKubeStateScrapeJob(namespace)
	}
	if nodeExporterInstalled {
		config += renderNodeExporterScrapeJob()
	}
	return config
}

// kubeletScrapeJob is one of the kubelet's metrics endpoints.
//
// The kubelet answers on three paths and they are three different datasets, not
// three spellings of one: `/metrics/resource` is the small, stable usage
// endpoint Kubernetes maintains as an API, `/metrics/cadvisor` is the container
// runtime's own view, and `/metrics` is the kubelet's internals. Only the first
// is small enough to take whole — the other two are taken through an allow list
// for the same reason the exporters are, and `keep` names exactly the families
// the query catalogue reads.
type kubeletScrapeJob struct {
	name string
	path string
	// keep is a regular expression over metric names. Empty takes the endpoint
	// whole, which is only ever right for `/metrics/resource`.
	keep string
	// honorTimestamps takes the timestamps the endpoint reports rather than the
	// moment of the scrape. Right for the resource endpoint, which Kubernetes
	// stamps from the sample it took; wrong for cAdvisor, whose housekeeping
	// runs on its own interval and whose stamps therefore arrive out of step
	// with the scrape that carried them.
	honorTimestamps bool
}

var kubeletScrapeJobs = []kubeletScrapeJob{
	{name: "kubelet-resource", path: "/metrics/resource", honorTimestamps: true},
	{
		// Throttling and per-Pod network, neither of which the resource endpoint
		// reports. A container held at its CPU limit shows no anomaly in usage —
		// it is using exactly what it was allowed — so throttled periods are the
		// only series that explains why it is slow.
		name: "kubelet-cadvisor",
		path: "/metrics/cadvisor",
		keep: "container_cpu_cfs_periods_total|" +
			"container_cpu_cfs_throttled_periods_total|" +
			"container_network_receive_bytes_total|" +
			"container_network_transmit_bytes_total",
	},
	{
		// Volume statistics. The kubelet is the only component that reports how
		// full a PersistentVolumeClaim is: kube-state-metrics knows the claim
		// exists and how large it was requested, and neither answers whether it
		// is about to fill up.
		name: "kubelet-volume",
		path: "/metrics",
		keep: "kubelet_volume_stats_available_bytes|" +
			"kubelet_volume_stats_capacity_bytes|" +
			"kubelet_volume_stats_used_bytes|" +
			"kubelet_volume_stats_inodes|" +
			"kubelet_volume_stats_inodes_used",
	},
}

func renderKubeletScrapeJob(job kubeletScrapeJob, port uint32) string {
	config := fmt.Sprintf(`  - job_name: %s
    scheme: https
    metrics_path: %s
    honor_timestamps: %t
    tls_config:
      # The kubelet serving certificate is usually signed by a CA the
      # ServiceAccount bundle does not contain. The connection stays inside the
      # Cluster and carries only the ServiceAccount token.
      insecure_skip_verify: true
    authorization:
      type: Bearer
      credentials_file: /var/run/secrets/kubernetes.io/serviceaccount/token
    kubernetes_sd_configs:
      - role: node
    relabel_configs:
      - source_labels: [__meta_kubernetes_node_name]
        target_label: node
      - source_labels: [__meta_kubernetes_node_address_InternalIP]
        target_label: __address__
        replacement: $1:%d
      # Scope labels come from the Server, which derives them from this Agent's
      # connection identity. Anything with the prefix would be dropped there, so
      # none is sent.
      - regex: ^zke_.*$
        action: labeldrop
`, job.name, job.path, job.honorTimestamps, port)
	if job.keep == "" {
		return config
	}
	// cAdvisor labels every series with the cgroup id, the image reference and
	// the runtime's container name. All three change on every restart of the
	// same container, which is how a Cluster's series count grows without its
	// workloads changing at all. Nothing in the catalogue reads them, and
	// dropping them on the volume endpoint too costs nothing: it never sets
	// them.
	return config + fmt.Sprintf(`    metric_relabel_configs:
      - source_labels: [__name__]
        regex: %s
        action: keep
      - regex: ^(id|image|name)$
        action: labeldrop
`, job.keep)
}

// renderKubeStateScrapeJob reaches the object metrics exporter through its
// Service. A name that does not move means the collector needs no permission to
// list Pods just to find one address.
func renderKubeStateScrapeJob(namespace string) string {
	return fmt.Sprintf(`  - job_name: kube-state-metrics
    scheme: http
    metrics_path: /metrics
    static_configs:
      - targets: ['%s.%s.svc:%d']
    relabel_configs:
      - regex: ^zke_.*$
        action: labeldrop
%s`, observability.KubeStateName, namespace, observability.KubeStatePort, containerStateFilter())
}

// containerStateFilter drops the container state series nothing queries.
//
// The two families report one series per container per reason, and the reasons
// nobody acts on outnumber the ones anybody does. Filtering them at the scrape
// rather than at the query is what keeps them out of storage every Cluster
// shares — a query-side selector would still have paid for them.
//
// Written as mark-then-drop because relabelling cannot express "not one of
// these": a temporary label is set on the series worth keeping, everything in
// those two families without it is dropped, and the label is removed again so
// it never reaches storage. Every other family passes through untouched.
func containerStateFilter() string {
	families := strings.Join(observability.ContainerStateFamilies, "|")
	reasons := strings.Join(append(
		append([]string{}, observability.ContainerWaitingReasons...),
		observability.ContainerTerminatedReasons...,
	), "|")
	return fmt.Sprintf(`    metric_relabel_configs:
      - source_labels: [__name__, reason]
        regex: (%s);(%s)
        target_label: __tmp_zke_keep
        replacement: "yes"
      - source_labels: [__name__, __tmp_zke_keep]
        regex: (%s);
        action: drop
      - regex: ^__tmp_zke_keep$
        action: labeldrop
`, families, reasons, families)
}

// renderNodeExporterScrapeJob discovers the exporter the same way the kubelet
// job discovers kubelets: it runs on the host network of every Node, so the
// Node's own address plus its port is where it answers. Reusing Node discovery
// means the collector needs no permission it does not already hold.
func renderNodeExporterScrapeJob() string {
	return fmt.Sprintf(`  - job_name: node-exporter
    scheme: http
    metrics_path: /metrics
    kubernetes_sd_configs:
      - role: node
    relabel_configs:
      - source_labels: [__meta_kubernetes_node_name]
        target_label: node
      - source_labels: [__meta_kubernetes_node_address_InternalIP]
        target_label: __address__
        replacement: $1:%d
      - regex: ^zke_.*$
        action: labeldrop
`, observability.NodeExporterPort)
}

const (
	nodeExporterReasonAnnotation  = "zke.io/node-exporter-unavailable-reason"
	nodeExporterMessageAnnotation = "zke.io/node-exporter-unavailable-message"
)

// nodeExporterAnnotations returns nil when the component installed, so a
// recovered install clears the note rather than leaving a stale explanation on
// a component that is now running.
func nodeExporterAnnotations(reason string, message string) map[string]string {
	if reason == "" {
		return nil
	}
	return map[string]string{
		nodeExporterReasonAnnotation:  reason,
		nodeExporterMessageAnnotation: message,
	}
}

func managedByAgent(labels map[string]string) bool {
	return labels[observability.ManagedByLabel] == observability.ManagedByAgent
}

func collectorImageOf(deployment *appsv1.Deployment) string {
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name == observability.CollectorContainerName {
			return container.Image
		}
	}
	return ""
}

func collectorFailure(
	code agentv1.ResultCode,
	status int32,
	reason string,
	message string,
) *agentv1.MetricsCollectorResponse {
	return &agentv1.MetricsCollectorResponse{
		Result:               code,
		KubernetesStatusCode: status,
		Reason:               reason,
		Message:              message,
	}
}

func collectorApplyFailure(kind string, err error) *agentv1.MetricsCollectorResponse {
	if strings.Contains(err.Error(), "not managed by ZKE") {
		return collectorFailure(
			agentv1.ResultCode_RESULT_CODE_CONFLICT,
			http.StatusConflict,
			"CollectorObjectNotManaged",
			"an existing "+kind+" with the collector name is not managed by ZKE",
		)
	}
	return collectorKubernetesFailure("apply "+kind, err)
}

// collectorKubernetesFailure maps a Kubernetes error without quoting it: the
// message can name objects and fields outside this operation, and it travels to
// a browser.
func collectorKubernetesFailure(
	operation string,
	err error,
) *agentv1.MetricsCollectorResponse {
	status := int32(http.StatusInternalServerError)
	code := agentv1.ResultCode_RESULT_CODE_INTERNAL
	reason := "CollectorOperationFailed"
	var statusError *apierrors.StatusError
	if errors.As(err, &statusError) {
		status = statusError.ErrStatus.Code
		reason = string(statusError.ErrStatus.Reason)
		switch {
		case apierrors.IsForbidden(err):
			code = agentv1.ResultCode_RESULT_CODE_FORBIDDEN
		case apierrors.IsConflict(err), apierrors.IsAlreadyExists(err):
			code = agentv1.ResultCode_RESULT_CODE_CONFLICT
		case apierrors.IsNotFound(err):
			code = agentv1.ResultCode_RESULT_CODE_NOT_FOUND
		}
	}
	if reason == "" {
		reason = "CollectorOperationFailed"
	}
	return collectorFailure(code, status, reason, "could not "+operation)
}

func pointerTo[T any](value T) *T {
	return &value
}
