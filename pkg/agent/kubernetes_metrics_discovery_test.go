package agent

import (
	"strings"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"github.com/togettoyou/zke/pkg/shared/observability"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func annotatedService(
	namespace string,
	name string,
	annotations map[string]string,
) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace, Name: name, Annotations: annotations,
	}}
}

// annotatedSlice is one EndpointSlice as a controller writes it: named after
// the Service with a generated suffix, and pointing back at it by label.
func annotatedSlice(
	namespace string,
	serviceName string,
	suffix string,
	annotations map[string]string,
	endpoints []discoveryv1.Endpoint,
	ports []discoveryv1.EndpointPort,
) *discoveryv1.EndpointSlice {
	return &discoveryv1.EndpointSlice{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   namespace,
			Name:        serviceName + "-" + suffix,
			Labels:      map[string]string{discoveryv1.LabelServiceName: serviceName},
			Annotations: annotations,
		},
		AddressType: discoveryv1.AddressTypeIPv4,
		Endpoints:   endpoints,
		Ports:       ports,
	}
}

func slicePort(port int32) []discoveryv1.EndpointPort {
	return []discoveryv1.EndpointPort{{Port: &port}}
}

func readyEndpoint(addresses ...string) discoveryv1.Endpoint {
	ready := true
	return discoveryv1.Endpoint{
		Addresses: addresses, Conditions: discoveryv1.EndpointConditions{Ready: &ready},
	}
}

func notReadyEndpoint(addresses ...string) discoveryv1.Endpoint {
	ready := false
	return discoveryv1.Endpoint{
		Addresses: addresses, Conditions: discoveryv1.EndpointConditions{Ready: &ready},
	}
}

func detailsRequest() *agentv1.MetricsCollectorRequest {
	return &agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_DETAILS,
	}
}

func jobByName(state *agentv1.MetricsCollectorState, name string) *agentv1.MetricsScrapeJob {
	for _, job := range state.GetScrapeJobs() {
		if job.GetJobName() == name {
			return job
		}
	}
	return nil
}

// The detail response has to describe the same targets the scrape configuration
// keeps. Reporting a job the collector never scrapes — or hiding one it does —
// turns this screen into a second, wrong source of truth about a Cluster.
func TestCollectorDetailsReportsBuiltInAndAnnotatedJobs(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(
		annotatedService("shop", "api", map[string]string{
			observability.ScrapeAnnotation:     "true",
			observability.ScrapePortAnnotation: "9102",
			observability.ScrapePathAnnotation: "/actuator/prometheus",
		}),
		// Two slices for one Service, the way a Service that outgrew one is
		// split. Both belong to the same job.
		annotatedSlice("shop", "api", "aaa", nil, []discoveryv1.Endpoint{
			readyEndpoint("10.1.0.4"), notReadyEndpoint("10.1.0.6"),
		}, slicePort(8080)),
		annotatedSlice("shop", "api", "bbb", nil, []discoveryv1.Endpoint{
			readyEndpoint("10.1.0.5"),
		}, slicePort(8080)),
		// Opted in on the slice alone, which is what a selectorless Service's
		// hand-maintained Endpoints looks like once the mirroring controller
		// has copied its annotations across.
		annotatedSlice("shop", "legacy", "ccc", map[string]string{
			observability.ScrapeAnnotation:       "true",
			observability.ScrapeSchemeAnnotation: "https",
			observability.ScrapeAuthAnnotation:   observability.ScrapeAuthServiceAccount,
		}, []discoveryv1.Endpoint{readyEndpoint("10.1.0.9")}, slicePort(9090)),
		annotatedService("shop", "quiet", nil),
	)
	handler := collectorHandler(client)
	if _, err := handler(bundleRequest()); err != nil {
		t.Fatal(err)
	}
	response, err := handler(detailsRequest())
	if err != nil {
		t.Fatal(err)
	}
	if response.GetResult() != agentv1.ResultCode_RESULT_CODE_OK {
		t.Fatalf("details response = %+v", response)
	}
	state := response.GetState()
	if state.GetScrapeJobsTruncated() {
		t.Fatal("a four-object Cluster reported a truncated job list")
	}
	for _, name := range []string{"kubelet", "kube-state-metrics", "node-exporter"} {
		job := jobByName(state, name)
		if job == nil || job.GetSourceKind() != "Builtin" {
			t.Fatalf("built-in job %q missing from details: %+v", name, state.GetScrapeJobs())
		}
	}

	api := jobByName(state, "shop/api")
	if api == nil {
		t.Fatalf("annotated Service missing from details: %+v", state.GetScrapeJobs())
	}
	// The annotated port replaces the Endpoints port, and unready addresses are
	// not scraped, so neither may appear as a target.
	if api.GetSourceKind() != "Service" || api.GetScheme() != "http" ||
		api.GetMetricsPath() != "/actuator/prometheus" || api.GetPort() != "9102" ||
		api.GetAuthentication() != observability.ScrapeAuthNone ||
		len(api.GetTargets()) != 2 ||
		api.GetTargets()[0] != "10.1.0.4:9102" || api.GetTargets()[1] != "10.1.0.5:9102" {
		t.Fatalf("annotated Service job = %+v", api)
	}

	legacy := jobByName(state, "shop/legacy")
	if legacy == nil || legacy.GetSourceKind() != "EndpointSlice" ||
		legacy.GetScheme() != "https" ||
		legacy.GetAuthentication() != observability.ScrapeAuthServiceAccount ||
		len(legacy.GetTargets()) != 1 || legacy.GetTargets()[0] != "10.1.0.9:9090" {
		t.Fatalf("annotated Endpoints job = %+v", legacy)
	}

	if jobByName(state, "shop/quiet") != nil {
		t.Fatalf("an unannotated Service was reported: %+v", state.GetScrapeJobs())
	}
	// The fleet list polls STATUS, and it must not pay for a cluster-wide
	// listing of every Service and Endpoints object.
	status, err := handler(&agentv1.MetricsCollectorRequest{
		Action: agentv1.MetricsCollectorAction_METRICS_COLLECTOR_ACTION_STATUS,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(status.GetState().GetScrapeJobs()) != 0 {
		t.Fatalf("status carried discovery results: %+v", status.GetState().GetScrapeJobs())
	}
}

// The slice is the more specific object, so its annotations win over the same
// annotation on the Service — which is exactly what the relabel rules do, and
// the two have to agree.
func TestCollectorDetailsPrefersEndpointAnnotations(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(
		annotatedService("shop", "api", map[string]string{
			observability.ScrapeAnnotation:       "true",
			observability.ScrapeSchemeAnnotation: "http",
			observability.ScrapePathAnnotation:   "/metrics",
		}),
		annotatedSlice("shop", "api", "aaa", map[string]string{
			observability.ScrapeSchemeAnnotation:      "https",
			observability.ScrapePathAnnotation:        "/telemetry",
			observability.ScrapeInsecureTLSAnnotation: "true",
		}, []discoveryv1.Endpoint{readyEndpoint("10.1.0.4")}, slicePort(8443)),
	)
	handler := collectorHandler(client)
	if _, err := handler(installRequest()); err != nil {
		t.Fatal(err)
	}
	response, err := handler(detailsRequest())
	if err != nil {
		t.Fatal(err)
	}
	job := jobByName(response.GetState(), "shop/api")
	if job == nil || job.GetScheme() != "https" || job.GetMetricsPath() != "/telemetry" ||
		!job.GetInsecureSkipVerify() || len(job.GetTargets()) != 1 ||
		job.GetTargets()[0] != "10.1.0.4:8443" {
		t.Fatalf("effective job = %+v", job)
	}
}

// Annotation values are Cluster input. A combination the scrape configuration
// refuses must not be reported as if something were collecting it.
func TestCollectorDetailsSkipsUnsupportedAnnotations(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(
		// A bearer token over cleartext would put the collector's own
		// ServiceAccount credential on the wire.
		annotatedService("shop", "plaintext-token", map[string]string{
			observability.ScrapeAnnotation:     "true",
			observability.ScrapeAuthAnnotation: observability.ScrapeAuthServiceAccount,
		}),
		annotatedService("shop", "unknown-auth", map[string]string{
			observability.ScrapeAnnotation:     "true",
			observability.ScrapeAuthAnnotation: "basic",
		}),
		// Skipping verification only means anything over TLS, and accepting it
		// over http would report a guarantee the scrape never made.
		annotatedService("shop", "insecure-plaintext", map[string]string{
			observability.ScrapeAnnotation:            "true",
			observability.ScrapeInsecureTLSAnnotation: "true",
		}),
		annotatedService("shop", "bad-scheme", map[string]string{
			observability.ScrapeAnnotation:       "true",
			observability.ScrapeSchemeAnnotation: "file",
		}),
		annotatedService("shop", "bad-path", map[string]string{
			observability.ScrapeAnnotation:     "true",
			observability.ScrapePathAnnotation: "metrics",
		}),
		annotatedService("shop", "bad-port", map[string]string{
			observability.ScrapeAnnotation:     "true",
			observability.ScrapePortAnnotation: "70000",
		}),
	)
	handler := collectorHandler(client)
	if _, err := handler(installRequest()); err != nil {
		t.Fatal(err)
	}
	response, err := handler(detailsRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range response.GetState().GetScrapeJobs() {
		if job.GetSourceKind() != "Builtin" {
			t.Fatalf("an unsupported annotation was reported as a job: %+v", job)
		}
	}
}

// The scrape configuration and the detail report read the same annotations, so
// they have to accept the same values. A value one of them takes and the other
// refuses is a Cluster whose screen contradicts what it is collecting.
func TestAnnotationGrammarIsSharedByScrapeConfigAndDetails(t *testing.T) {
	t.Parallel()

	config := renderAnnotatedEndpointScrapeJobs()
	for _, pattern := range []string{
		scrapeSchemePattern, scrapePathPattern, scrapePortPattern,
	} {
		if !strings.Contains(config, `regex: "(`+pattern+`)"`) {
			t.Fatalf("scrape configuration does not enforce %q:\n%s", pattern, config)
		}
	}
	for value, accepted := range map[string]bool{
		"":      true,
		"1":     true,
		"80":    true,
		"8080":  true,
		"10250": true,
		"65535": true,
		// Leading zeros and signs are refused rather than normalized: the two
		// sides would otherwise have to agree on how to normalize them.
		"080":   false,
		"+80":   false,
		"0":     false,
		"65536": false,
		"70000": false,
		"http":  false,
	} {
		if scrapePortExpression.MatchString(value) != accepted {
			t.Fatalf("port %q acceptance = %v", value, !accepted)
		}
	}
	for value, accepted := range map[string]bool{
		"": true, "http": true, "https": true, "HTTP": false, "file": false, "httpx": false,
	} {
		if scrapeSchemeExpression.MatchString(value) != accepted {
			t.Fatalf("scheme %q acceptance = %v", value, !accepted)
		}
	}
	for value, accepted := range map[string]bool{
		"": true, "/metrics": true, "/actuator/prometheus": true,
		"metrics": false, "http://host/metrics": false, "/a\nb": false,
	} {
		if scrapePathExpression.MatchString(value) != accepted {
			t.Fatalf("path %q acceptance = %v", value, !accepted)
		}
	}
}

// A hand-written slice that leaves the ready condition unset is not scraped.
//
// Kubernetes reads an unset condition as ready, but vmagent renders it with
// strconv.FormatBool over a non-pointer bool, so it reaches the relabel rules
// as "false" and the target is dropped. The report has to say what is being
// collected, not what the API's default would suggest.
func TestCollectorDetailsFollowsTheCollectorOnUnsetReadiness(t *testing.T) {
	t.Parallel()

	client := fake.NewClientset(
		annotatedService("shop", "api", map[string]string{
			observability.ScrapeAnnotation: "true",
		}),
		annotatedSlice("shop", "api", "aaa", nil, []discoveryv1.Endpoint{
			{Addresses: []string{"10.1.0.4"}},
			readyEndpoint("10.1.0.5"),
		}, slicePort(8080)),
	)
	handler := collectorHandler(client)
	if _, err := handler(installRequest()); err != nil {
		t.Fatal(err)
	}
	response, err := handler(detailsRequest())
	if err != nil {
		t.Fatal(err)
	}
	job := jobByName(response.GetState(), "shop/api")
	if job == nil || len(job.GetTargets()) != 1 || job.GetTargets()[0] != "10.1.0.5:8080" {
		t.Fatalf("targets = %+v", job)
	}
}

// A slice that does not say which Service it backs cannot be attributed to a
// job an operator would recognise, and the scrape configuration keys its job
// on the same label, so neither side collects it.
func TestCollectorDetailsIgnoresSlicesWithoutAService(t *testing.T) {
	t.Parallel()

	orphan := annotatedSlice("shop", "api", "aaa", map[string]string{
		observability.ScrapeAnnotation: "true",
	}, []discoveryv1.Endpoint{readyEndpoint("10.1.0.4")}, slicePort(8080))
	orphan.Labels = nil
	client := fake.NewClientset(orphan)
	handler := collectorHandler(client)
	if _, err := handler(installRequest()); err != nil {
		t.Fatal(err)
	}
	response, err := handler(detailsRequest())
	if err != nil {
		t.Fatal(err)
	}
	for _, job := range response.GetState().GetScrapeJobs() {
		if job.GetSourceKind() != "Builtin" {
			t.Fatalf("an unattributable slice was reported as a job: %+v", job)
		}
	}
}
