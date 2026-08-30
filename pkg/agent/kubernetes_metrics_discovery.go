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
	discoveryv1 "k8s.io/api/discovery/v1"
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
	sliceMeta := "__meta_kubernetes_endpointslice_annotation_zke_metrics_collector_io_"
	// How a slice says which Service it backs. It is present on every slice a
	// controller writes and required on a hand-written one, which makes it a
	// safer key than the Service object: a slice can outlive its Service.
	sliceServiceName := "__meta_kubernetes_endpointslice_label_kubernetes_io_service_name"
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
      - role: endpointslice
    relabel_configs:
      # EndpointSlice annotations override the same annotation inherited from
      # the Service. A missing override leaves the Service value in place. The
      # mirroring controller copies a hand-maintained Endpoints object's
      # annotations onto its slices, so a selectorless Service is annotated the
      # same way it always was.
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
      - source_labels: [__meta_kubernetes_endpointslice_endpoint_conditions_ready]
        regex: "true"
        action: keep
`,
		serviceMeta, sliceMeta,
		serviceMeta, sliceMeta,
		serviceMeta, sliceMeta,
		serviceMeta, sliceMeta,
		serviceMeta, sliceMeta,
		serviceMeta, sliceMeta,
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
	// The slice's own name is deliberately not a label. It carries a generated
	// suffix and is replaced whenever the controller regenerates it, so keeping
	// it would start a new series on every regeneration — the one cost the
	// Cluster pays for the whole retention window rather than per scrape.
	return config + fmt.Sprintf(`      - source_labels: [__tmp_zke_scheme]
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
      - source_labels: [%s]
        target_label: service
      - source_labels: [__meta_kubernetes_namespace, %s]
        separator: /
        target_label: job
      - regex: ^(__tmp_zke_.*|zke_.*)$
        action: labeldrop
`, sliceServiceName, sliceServiceName)
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

// annotatedSource is one collected job's worth of objects: the Service that
// names it and the EndpointSlices that back it.
//
// Keyed by Service rather than by slice because slice names are generated and
// a Service is split across several of them once it grows. Putting a slice name
// on a series would churn it on every regeneration, which is the cost the
// collector's own budget is least able to absorb.
type annotatedSource struct {
	namespace string
	name      string
	service   *corev1.Service
	slices    []*discoveryv1.EndpointSlice
}

func discoverAnnotatedEndpointJobs(
	ctx context.Context,
	client kubernetes.Interface,
) ([]*agentv1.MetricsScrapeJob, bool, error) {
	services, err := client.CoreV1().Services(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, false, err
	}
	slices, err := client.DiscoveryV1().EndpointSlices(metav1.NamespaceAll).List(
		ctx, metav1.ListOptions{},
	)
	if err != nil {
		return nil, false, err
	}
	sources := make(map[string]*annotatedSource, len(services.Items))
	source := func(namespace, name string) *annotatedSource {
		key := objectKey(namespace, name)
		if existing, found := sources[key]; found {
			return existing
		}
		created := &annotatedSource{namespace: namespace, name: name}
		sources[key] = created
		return created
	}
	for index := range services.Items {
		service := &services.Items[index]
		source(service.Namespace, service.Name).service = service
	}
	for index := range slices.Items {
		slice := &slices.Items[index]
		// The label is how a slice says which Service it backs, and it is what
		// the scrape configuration keys its job on. A slice without it cannot be
		// attributed to anything an operator would recognise.
		serviceName := slice.Labels[discoveryv1.LabelServiceName]
		if serviceName == "" {
			continue
		}
		owner := source(slice.Namespace, serviceName)
		owner.slices = append(owner.slices, slice)
	}

	enabled := make([]*annotatedSource, 0, len(sources))
	for _, candidate := range sources {
		sort.Slice(candidate.slices, func(i, j int) bool {
			return candidate.slices[i].Name < candidate.slices[j].Name
		})
		if sourceEnabled(candidate) {
			enabled = append(enabled, candidate)
		}
	}
	sort.Slice(enabled, func(i, j int) bool {
		return objectKey(enabled[i].namespace, enabled[i].name) <
			objectKey(enabled[j].namespace, enabled[j].name)
	})

	jobs := make([]*agentv1.MetricsScrapeJob, 0, min(len(enabled), maxReportedScrapeJobs))
	truncated := false
	for _, candidate := range enabled {
		if len(jobs) >= maxReportedScrapeJobs {
			truncated = true
			break
		}
		job, ok := annotatedSourceJob(candidate)
		if ok {
			jobs = append(jobs, job)
		}
	}
	return jobs, truncated, nil
}

func sourceEnabled(source *annotatedSource) bool {
	annotations := effectiveScrapeAnnotations(source)
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

func annotatedSourceJob(source *annotatedSource) (*agentv1.MetricsScrapeJob, bool) {
	annotations := effectiveScrapeAnnotations(source)
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
	targets, targetsTruncated := endpointSliceTargets(source.slices, port)
	return &agentv1.MetricsScrapeJob{
		JobName:            source.namespace + "/" + source.name,
		SourceKind:         scrapeSourceKind(source),
		Namespace:          source.namespace,
		SourceName:         source.name,
		Scheme:             scheme,
		MetricsPath:        path,
		Port:               port,
		Authentication:     auth,
		InsecureSkipVerify: annotations[observability.ScrapeInsecureTLSAnnotation] == "true",
		Targets:            targets,
		TargetsTruncated:   targetsTruncated,
	}, true
}

// scrapeSourceKind names the object that opted this job in, which is the one an
// operator has to edit to change or stop it. A Service's annotations are
// mirrored onto the slices of a selectorless Service, so a slice carrying the
// switch itself is the case where the Service is not where it was written.
func scrapeSourceKind(source *annotatedSource) string {
	for _, slice := range source.slices {
		if slice.Annotations[observability.ScrapeAnnotation] != "" {
			return "EndpointSlice"
		}
	}
	if source.service != nil {
		return "Service"
	}
	return "EndpointSlice"
}

// effectiveScrapeAnnotations resolves the Service's annotations against the
// slices', the way the relabel rules resolve them: the more specific object
// wins, and an annotation it does not set leaves the Service's value in place.
//
// Slices are read in name order so a Service split across several of them
// resolves the same way every time. In practice they agree: the mirroring
// controller copies one Endpoints object's annotations onto all of them, and
// controller-generated slices carry none at all.
func effectiveScrapeAnnotations(source *annotatedSource) map[string]string {
	result := make(map[string]string)
	if source.service != nil {
		for key, value := range source.service.Annotations {
			result[key] = strings.TrimSpace(value)
		}
	}
	for _, slice := range source.slices {
		for key, value := range slice.Annotations {
			result[key] = strings.TrimSpace(value)
		}
	}
	return result
}

// endpointSliceTargets is what the collector will actually scrape.
//
// A nil `ready` condition counts as not ready, which is stricter than the
// Kubernetes default of treating it as ready. It is what vmagent does — it
// renders the condition with strconv.FormatBool over a non-pointer bool, so an
// unset one reaches the relabel rules as "false" — and this list has to say
// what is being scraped rather than what ought to be.
func endpointSliceTargets(slices []*discoveryv1.EndpointSlice, overridePort string) ([]string, bool) {
	unique := make(map[string]struct{})
	for _, slice := range slices {
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready == nil || !*endpoint.Conditions.Ready {
				continue
			}
			for _, address := range endpoint.Addresses {
				if overridePort != "" {
					unique[net.JoinHostPort(address, overridePort)] = struct{}{}
					continue
				}
				for _, port := range slice.Ports {
					if port.Port == nil {
						continue
					}
					unique[net.JoinHostPort(address, strconv.Itoa(int(*port.Port)))] = struct{}{}
				}
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
