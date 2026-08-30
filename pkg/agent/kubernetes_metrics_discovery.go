package agent

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	annotatedEndpointJobName = "zke-annotated-endpoints"
	maxReportedScrapeJobs    = 500
	maxReportedJobTargets    = 200

	// The vocabulary the three free-form annotations accept, written once so
	// the scrape configuration and the detail report cannot drift: the first
	// decides what is collected, the second describes it, and a Cluster whose
	// screen disagrees with its collector is worse than one with neither.
	// The empty alternative is the annotation being absent, which leaves the
	// job default in place.
	scrapeSchemePattern = "|http|https"
	scrapePathPattern   = "|/.*"
	scrapePortPattern   = "|[1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|" +
		"65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5]"
)

// Anchored the way relabeling anchors: a value matches in full or not at all.
var (
	scrapeSchemeExpression = regexp.MustCompile(`\A(?:` + scrapeSchemePattern + `)\z`)
	scrapePathExpression   = regexp.MustCompile(`\A(?:` + scrapePathPattern + `)\z`)
	scrapePortExpression   = regexp.MustCompile(`\A(?:` + scrapePortPattern + `)\z`)
)

// renderAnnotatedEndpointScrapeJobs opts Services and legacy Endpoints into
// collection without admitting arbitrary scrape configuration. Kubernetes SD
// keeps the target set current after installation; relabeling only accepts the
// small annotation vocabulary ZKE owns.
//
// Authentication and TLS verification are scrape-config properties rather
// than target labels, so four fixed jobs partition targets by those two modes.
// A ServiceAccount token is only sent over HTTPS. Arbitrary Secret references
// are deliberately not supported: mounting or reading them would turn a
// metrics annotation into a cluster credential reader.
func renderAnnotatedEndpointScrapeJobs() string {
	variants := []struct {
		name     string
		auth     string
		insecure bool
	}{
		{name: annotatedEndpointJobName, auth: observability.ScrapeAuthNone},
		{name: annotatedEndpointJobName + "-insecure", auth: observability.ScrapeAuthNone, insecure: true},
		{name: annotatedEndpointJobName + "-service-account", auth: observability.ScrapeAuthServiceAccount},
		{name: annotatedEndpointJobName + "-service-account-insecure", auth: observability.ScrapeAuthServiceAccount, insecure: true},
	}
	var config strings.Builder
	for _, variant := range variants {
		config.WriteString(renderAnnotatedEndpointScrapeJob(
			variant.name, variant.auth, variant.insecure,
		))
	}
	return config.String()
}

func renderAnnotatedEndpointScrapeJob(jobName string, auth string, insecure bool) string {
	serviceMeta := "__meta_kubernetes_service_annotation_zke_metrics_collector_io_"
	endpointMeta := "__meta_kubernetes_endpoints_annotation_zke_metrics_collector_io_"
	config := fmt.Sprintf(`  - job_name: %s
    scheme: http
    metrics_path: /metrics
`, jobName)
	if auth == observability.ScrapeAuthServiceAccount {
		config += `    authorization:
      type: Bearer
      credentials_file: /var/run/secrets/kubernetes.io/serviceaccount/token
`
	}
	if insecure {
		config += `    tls_config:
      insecure_skip_verify: true
`
	} else if auth == observability.ScrapeAuthServiceAccount {
		config += `    tls_config:
      ca_file: /var/run/secrets/kubernetes.io/serviceaccount/ca.crt
`
	}
	config += fmt.Sprintf(`    kubernetes_sd_configs:
      - role: endpoints
    relabel_configs:
      # Endpoints annotations override the same annotation inherited from its
      # Service. A missing override leaves the Service value in place.
      - source_labels: [%sscrape]
        regex: (.+)
        target_label: __tmp_zke_scrape
      - source_labels: [%sscrape]
        regex: (.+)
        target_label: __tmp_zke_scrape
      - source_labels: [%sscheme]
        regex: (.+)
        target_label: __tmp_zke_scheme
      - source_labels: [%sscheme]
        regex: (.+)
        target_label: __tmp_zke_scheme
      - source_labels: [%spath]
        regex: (.+)
        target_label: __tmp_zke_path
      - source_labels: [%spath]
        regex: (.+)
        target_label: __tmp_zke_path
      - source_labels: [%sport]
        regex: (.+)
        target_label: __tmp_zke_port
      - source_labels: [%sport]
        regex: (.+)
        target_label: __tmp_zke_port
      - source_labels: [%sauth]
        regex: (.+)
        target_label: __tmp_zke_auth
      - source_labels: [%sauth]
        regex: (.+)
        target_label: __tmp_zke_auth
      - source_labels: [%stls_insecure_skip_verify]
        regex: (.+)
        target_label: __tmp_zke_insecure
      - source_labels: [%stls_insecure_skip_verify]
        regex: (.+)
        target_label: __tmp_zke_insecure
      - source_labels: [__tmp_zke_scrape]
        regex: "true"
        action: keep
      # A value ZKE does not understand drops the target rather than falling
      # back to a default. Silently scraping /metrics because somebody wrote
      # "metrics" would collect a path nobody asked for, and would disagree
      # with what collector details say is being collected.
      - source_labels: [__tmp_zke_scheme]
        regex: "(%s)"
        action: keep
      - source_labels: [__tmp_zke_path]
        regex: "(%s)"
        action: keep
      - source_labels: [__tmp_zke_port]
        regex: "(%s)"
        action: keep
      - source_labels: [__meta_kubernetes_endpoint_ready]
        regex: "true"
        action: keep
`,
		serviceMeta, endpointMeta,
		serviceMeta, endpointMeta,
		serviceMeta, endpointMeta,
		serviceMeta, endpointMeta,
		serviceMeta, endpointMeta,
		serviceMeta, endpointMeta,
		scrapeSchemePattern, scrapePathPattern, scrapePortPattern,
	)
	if auth == observability.ScrapeAuthServiceAccount {
		config += `      - source_labels: [__tmp_zke_auth]
        regex: service-account
        action: keep
      - source_labels: [__tmp_zke_scheme]
        regex: https
        action: keep
`
	} else {
		config += `      - source_labels: [__tmp_zke_auth]
        regex: "|none"
        action: keep
`
	}
	if insecure {
		config += `      - source_labels: [__tmp_zke_insecure]
        regex: "true"
        action: keep
      - source_labels: [__tmp_zke_scheme]
        regex: https
        action: keep
`
	} else {
		config += `      - source_labels: [__tmp_zke_insecure]
        regex: "|false"
        action: keep
`
	}
	return config + `      - source_labels: [__tmp_zke_scheme]
        regex: (https?)
        target_label: __scheme__
      - source_labels: [__tmp_zke_path]
        regex: (/.*)
        target_label: __metrics_path__
      - source_labels: [__address__, __tmp_zke_port]
        regex: (.+):[0-9]+;([0-9]+)
        replacement: $1:$2
        target_label: __address__
      - source_labels: [__meta_kubernetes_namespace]
        target_label: namespace
      - source_labels: [__meta_kubernetes_service_name]
        target_label: service
      - source_labels: [__meta_kubernetes_endpoints_name]
        target_label: endpoint
      - source_labels: [__meta_kubernetes_namespace, __meta_kubernetes_endpoints_name]
        separator: /
        target_label: job
      - regex: ^(__tmp_zke_.*|zke_.*)$
        action: labeldrop
`
}

// collectorDetails adds the expensive cluster-wide discovery inventory to the
// ordinary collector state. It is called only from the detail endpoint, never
// from the fleet list's status poll.
func collectorDetails(
	ctx context.Context,
	client kubernetes.Interface,
	namespace string,
) (*agentv1.MetricsCollectorResponse, error) {
	response, err := collectorStatus(ctx, client, namespace)
	if err != nil || response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK ||
		!response.GetState().GetInstalled() {
		return response, err
	}
	configMap, err := client.CoreV1().ConfigMaps(namespace).Get(
		ctx, observability.CollectorConfigMapName, metav1.GetOptions{},
	)
	if err != nil {
		return collectorKubernetesFailure("read metrics collector ConfigMap", err), nil
	}
	config := configMap.Data[observability.CollectorConfigKey]
	jobs := builtInScrapeJobDetails(namespace, config)
	truncated := false
	if strings.Contains(config, "job_name: "+annotatedEndpointJobName) {
		discovered, cut, discoveryErr := discoverAnnotatedEndpointJobs(ctx, client)
		if discoveryErr != nil {
			return collectorKubernetesFailure("discover annotated metrics endpoints", discoveryErr), nil
		}
		jobs = append(jobs, discovered...)
		truncated = cut
	}
	sort.Slice(jobs, func(i, j int) bool {
		if jobs[i].GetSourceKind() != jobs[j].GetSourceKind() {
			return jobs[i].GetSourceKind() < jobs[j].GetSourceKind()
		}
		if jobs[i].GetNamespace() != jobs[j].GetNamespace() {
			return jobs[i].GetNamespace() < jobs[j].GetNamespace()
		}
		return jobs[i].GetJobName() < jobs[j].GetJobName()
	})
	if len(jobs) > maxReportedScrapeJobs {
		jobs = jobs[:maxReportedScrapeJobs]
		truncated = true
	}
	response.State.ScrapeJobs = jobs
	response.State.ScrapeJobsTruncated = truncated
	return response, nil
}

func builtInScrapeJobDetails(namespace string, config string) []*agentv1.MetricsScrapeJob {
	candidates := []*agentv1.MetricsScrapeJob{
		{JobName: "kubelet-resource", SourceKind: "Builtin", Scheme: "https", MetricsPath: "/metrics/resource", Port: "10250", Authentication: observability.ScrapeAuthServiceAccount},
		{JobName: "kubelet-cadvisor", SourceKind: "Builtin", Scheme: "https", MetricsPath: "/metrics/cadvisor", Port: "10250", Authentication: observability.ScrapeAuthServiceAccount},
		{JobName: "kubelet", SourceKind: "Builtin", Scheme: "https", MetricsPath: "/metrics", Port: "10250", Authentication: observability.ScrapeAuthServiceAccount},
		{JobName: "kube-state-metrics", SourceKind: "Builtin", Scheme: "http", MetricsPath: "/metrics", Port: strconv.Itoa(observability.KubeStatePort), Authentication: observability.ScrapeAuthNone, Targets: []string{fmt.Sprintf("%s.%s.svc:%d", observability.KubeStateName, namespace, observability.KubeStatePort)}},
		{JobName: "node-exporter", SourceKind: "Builtin", Scheme: "http", MetricsPath: "/metrics", Port: strconv.Itoa(observability.NodeExporterPort), Authentication: observability.ScrapeAuthNone},
	}
	jobs := make([]*agentv1.MetricsScrapeJob, 0, len(candidates))
	for _, job := range candidates {
		if strings.Contains(config, "job_name: "+job.GetJobName()+"\n") {
			jobs = append(jobs, job)
		}
	}
	return jobs
}

type annotatedSource struct {
	service  *corev1.Service
	endpoint *corev1.Endpoints
}

func discoverAnnotatedEndpointJobs(
	ctx context.Context,
	client kubernetes.Interface,
) ([]*agentv1.MetricsScrapeJob, bool, error) {
	services, err := client.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false, err
	}
	endpoints, err := client.CoreV1().Endpoints(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false, err
	}
	serviceByKey := make(map[string]*corev1.Service, len(services.Items))
	for index := range services.Items {
		service := &services.Items[index]
		serviceByKey[objectKey(service.Namespace, service.Name)] = service
	}
	endpointByKey := make(map[string]*corev1.Endpoints, len(endpoints.Items))
	for index := range endpoints.Items {
		endpoint := &endpoints.Items[index]
		endpointByKey[objectKey(endpoint.Namespace, endpoint.Name)] = endpoint
	}

	sources := make([]annotatedSource, 0)
	seen := make(map[string]struct{})
	for key, service := range serviceByKey {
		endpoint := endpointByKey[key]
		if sourceEnabled(service, endpoint) {
			sources = append(sources, annotatedSource{service: service, endpoint: endpoint})
			seen[key] = struct{}{}
		}
	}
	for key, endpoint := range endpointByKey {
		if _, ok := seen[key]; ok {
			continue
		}
		if sourceEnabled(nil, endpoint) {
			sources = append(sources, annotatedSource{endpoint: endpoint})
		}
	}
	sort.Slice(sources, func(i, j int) bool {
		left := sources[i].endpoint
		right := sources[j].endpoint
		if left == nil {
			left = &corev1.Endpoints{ObjectMeta: sources[i].service.ObjectMeta}
		}
		if right == nil {
			right = &corev1.Endpoints{ObjectMeta: sources[j].service.ObjectMeta}
		}
		return objectKey(left.Namespace, left.Name) < objectKey(right.Namespace, right.Name)
	})

	jobs := make([]*agentv1.MetricsScrapeJob, 0, min(len(sources), maxReportedScrapeJobs))
	truncated := false
	for _, source := range sources {
		if len(jobs) >= maxReportedScrapeJobs {
			truncated = true
			break
		}
		job, ok := annotatedSourceJob(source)
		if ok {
			jobs = append(jobs, job)
		}
	}
	return jobs, truncated, nil
}

func sourceEnabled(service *corev1.Service, endpoint *corev1.Endpoints) bool {
	annotations := effectiveScrapeAnnotations(service, endpoint)
	if annotations[observability.ScrapeAnnotation] != "true" {
		return false
	}
	auth := annotations[observability.ScrapeAuthAnnotation]
	if auth != "" && auth != observability.ScrapeAuthNone &&
		auth != observability.ScrapeAuthServiceAccount {
		return false
	}
	if auth == observability.ScrapeAuthServiceAccount &&
		annotations[observability.ScrapeSchemeAnnotation] != "https" {
		return false
	}
	insecure := annotations[observability.ScrapeInsecureTLSAnnotation]
	return insecure == "" || insecure == "false" ||
		(insecure == "true" && annotations[observability.ScrapeSchemeAnnotation] == "https")
}

func annotatedSourceJob(source annotatedSource) (*agentv1.MetricsScrapeJob, bool) {
	annotations := effectiveScrapeAnnotations(source.service, source.endpoint)
	scheme := annotations[observability.ScrapeSchemeAnnotation]
	path := annotations[observability.ScrapePathAnnotation]
	port := annotations[observability.ScrapePortAnnotation]
	if !scrapeSchemeExpression.MatchString(scheme) ||
		!scrapePathExpression.MatchString(path) ||
		!scrapePortExpression.MatchString(port) {
		return nil, false
	}
	// An absent annotation leaves the job default, which is what the scrape
	// configuration falls back to for the same target.
	if scheme == "" {
		scheme = "http"
	}
	if path == "" {
		path = "/metrics"
	}
	auth := annotations[observability.ScrapeAuthAnnotation]
	if auth == "" {
		auth = observability.ScrapeAuthNone
	}
	name, namespace, kind := "", "", "Service"
	if source.service != nil {
		name, namespace = source.service.Name, source.service.Namespace
	}
	if source.endpoint != nil && source.endpoint.Annotations[observability.ScrapeAnnotation] != "" {
		name, namespace, kind = source.endpoint.Name, source.endpoint.Namespace, "Endpoints"
	} else if name == "" && source.endpoint != nil {
		name, namespace, kind = source.endpoint.Name, source.endpoint.Namespace, "Endpoints"
	}
	targets, targetsTruncated := endpointTargets(source.endpoint, port)
	return &agentv1.MetricsScrapeJob{
		JobName:            namespace + "/" + name,
		SourceKind:         kind,
		Namespace:          namespace,
		SourceName:         name,
		Scheme:             scheme,
		MetricsPath:        path,
		Port:               port,
		Authentication:     auth,
		InsecureSkipVerify: annotations[observability.ScrapeInsecureTLSAnnotation] == "true",
		Targets:            targets,
		TargetsTruncated:   targetsTruncated,
	}, true
}

func effectiveScrapeAnnotations(
	service *corev1.Service,
	endpoint *corev1.Endpoints,
) map[string]string {
	result := make(map[string]string)
	if service != nil {
		for key, value := range service.Annotations {
			result[key] = strings.TrimSpace(value)
		}
	}
	if endpoint != nil {
		for key, value := range endpoint.Annotations {
			result[key] = strings.TrimSpace(value)
		}
	}
	return result
}

func endpointTargets(endpoint *corev1.Endpoints, overridePort string) ([]string, bool) {
	if endpoint == nil {
		return nil, false
	}
	unique := make(map[string]struct{})
	for _, subset := range endpoint.Subsets {
		for _, address := range subset.Addresses {
			if overridePort != "" {
				unique[net.JoinHostPort(address.IP, overridePort)] = struct{}{}
				continue
			}
			for _, port := range subset.Ports {
				unique[net.JoinHostPort(address.IP, strconv.Itoa(int(port.Port)))] = struct{}{}
			}
		}
	}
	targets := make([]string, 0, len(unique))
	for target := range unique {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	truncated := len(targets) > maxReportedJobTargets
	if truncated {
		targets = targets[:maxReportedJobTargets]
	}
	return targets, truncated
}

func objectKey(namespace string, name string) string {
	return namespace + "\x00" + name
}
