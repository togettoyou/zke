package metricsquery

import (
	"bytes"
	"context"
	"log/slog"
	"math"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang/snappy"
	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/store"
	"google.golang.org/protobuf/encoding/protowire"
)

// The catalogue's expressions are only really checked by a real backend: a
// template that a test double never evaluates can still be a PromQL error, and
// the metric names come from the kubelet rather than from anything this
// repository controls.
//
// Point ZKE_TEST_METRICS_STORAGE_URL at a disposable instance, for example
//
//	docker run -d -p 8428:8428 victoriametrics/victoria-metrics:v1.149.0
//	ZKE_TEST_METRICS_STORAGE_URL=http://127.0.0.1:8428 go test ./pkg/server/metricsquery/
func TestCatalogueQueriesRunAgainstRealStorage(t *testing.T) {
	base := strings.TrimRight(os.Getenv("ZKE_TEST_METRICS_STORAGE_URL"), "/")
	if base == "" {
		t.Skip("ZKE_TEST_METRICS_STORAGE_URL is not configured")
	}
	clusterID := "00000000-0000-4000-8000-0000000000c1"
	seedKubeletSamples(t, base, clusterID)

	service, err := NewService(
		Config{QueryURL: base + "/prometheus", MinStep: 15 * time.Second},
		stubVisibility{visibility: Visibility{Global: true}},
		stubClusters{scopes: []store.ClusterScope{
			{ClusterID: clusterID, ClusterName: "probe", Status: "active"},
		}},
		slog.New(slog.DiscardHandler),
	)
	if err != nil {
		t.Fatal(err)
	}

	end := time.Now().UTC().Truncate(time.Minute)
	// VictoriaMetrics accepts a write before it is queryable, so the seed is
	// waited for once here rather than in every subtest below. Everything after
	// this point may treat an empty answer as the query's fault.
	memory := waitForSeed(t, service, clusterID, end)

	// The whole catalogue, read from the catalogue itself rather than listed
	// here: a query added without a line in this test would otherwise ship
	// unevaluated, which is the one thing this test exists to prevent. `top` is
	// supplied wherever the definition allows it; a query that does not is asked
	// unbounded, which is how the Console asks for Cluster totals.
	for _, definition := range Catalog() {
		definition := definition
		t.Run(definition.Name, func(t *testing.T) {
			input := Input{UserID: userID, Name: definition.Name, ClusterID: clusterID}
			if definition.SupportsTop {
				input.Top = 10
			}
			if definition.Kind == KindRange {
				input.Start = end.Add(-10 * time.Minute)
				input.End = end
				input.Step = time.Minute
			}
			result, err := service.Query(context.Background(), input)
			if err != nil {
				t.Fatalf("%s failed against storage: %v", definition.Name, err)
			}
			// A template that parses is not a template that reads anything. The
			// metric names, their labels and the joins between them come from
			// the kubelet and the two exporters rather than from this
			// repository, and a query naming a family nobody scrapes, or joining
			// on a label one side does not carry, answers nothing without ever
			// failing. In the Console that is an empty chart on a Cluster whose
			// collection is healthy — the one outcome no error path reports. The
			// fixture therefore seeds every family the catalogue reads, and
			// every entry has to come back with a series.
			if len(result.Series) == 0 {
				t.Fatalf(
					"%s selected nothing from the seeded Cluster; "+
						"either the expression reads a family the fixture does not "+
						"write, or the fixture is missing a family the pipeline collects",
					definition.Name,
				)
			}
			if definition.Kind != KindRange {
				return
			}
			// The grid is produced by this Server, so it must be complete even
			// where storage returned nothing.
			for _, series := range result.Series {
				if len(series.Points) != 11 {
					t.Fatalf(
						"%s returned %d points, want the full grid",
						definition.Name,
						len(series.Points),
					)
				}
			}
		})
	}

	// A Namespace filter changes the expression rather than the parameters, so
	// every query that accepts one is evaluated a second time with it set.
	for _, definition := range Catalog() {
		if !definition.SupportsNamespace {
			continue
		}
		definition := definition
		t.Run(definition.Name+"/namespace", func(t *testing.T) {
			input := Input{
				UserID:    userID,
				Name:      definition.Name,
				ClusterID: clusterID,
				Namespace: "kube-system",
				Start:     end.Add(-10 * time.Minute),
				End:       end,
				Step:      time.Minute,
			}
			if definition.SupportsTop {
				input.Top = 10
			}
			result, err := service.Query(context.Background(), input)
			if err != nil {
				t.Fatalf("%s with a Namespace failed: %v", definition.Name, err)
			}
			// Every seeded object is in kube-system, so the filtered expression
			// has to select what the unfiltered one did. An injection that lands
			// in the wrong selector still parses and still returns nothing.
			if len(result.Series) == 0 {
				t.Fatalf(
					"%s selected nothing once filtered to the seeded Namespace",
					definition.Name,
				)
			}
		})
	}

	if len(memory.Series) != 1 {
		t.Fatalf("cluster_memory_usage returned %d series, want the seeded Cluster", len(memory.Series))
	}
	if memory.Series[0].ClusterID != clusterID {
		t.Fatalf("series Cluster = %q", memory.Series[0].ClusterID)
	}
	if memory.Series[0].ClusterName != "probe" {
		t.Fatalf("series Cluster name = %q", memory.Series[0].ClusterName)
	}

	// The rollup has to name the Deployment. Reporting the ReplicaSet would make
	// the same workload look like a different one after every rollout, and a
	// template that compiles cannot tell anyone whether the two-level join
	// actually resolved.
	workload, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "workload_memory_usage",
		Start:     end.Add(-10 * time.Minute),
		End:       end,
		Step:      time.Minute,
		Top:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(workload.Series) != 1 {
		t.Fatalf("workload_memory_usage returned %d series, want the seeded Deployment", len(workload.Series))
	}
	labels := workload.Series[0].Labels
	if labels["workload"] != "probe-app" || labels["workload_kind"] != "Deployment" {
		t.Fatalf("workload labels = %v, want the Deployment behind the ReplicaSet", labels)
	}
	if labels["namespace"] != "kube-system" {
		t.Fatalf("workload namespace = %q", labels["namespace"])
	}

	// Utilisation needs both sides of the division to survive the join.
	utilization, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "node_memory_utilization",
		Start:     end.Add(-10 * time.Minute),
		End:       end,
		Step:      time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(utilization.Series) != 1 {
		t.Fatalf("node_memory_utilization returned %d series, want the seeded Node", len(utilization.Series))
	}
	last := utilization.Series[0].Points[len(utilization.Series[0].Points)-1]
	// 1 GiB working set against 8 GiB allocatable.
	if last.Value == nil || math.Abs(*last.Value-0.125) > 0.001 {
		t.Fatalf("node_memory_utilization last point = %+v, want 0.125", last)
	}

	// The headline row is one query carrying several numbers under a label it
	// writes itself. A union whose branches collide would return fewer series
	// than it has branches, and the row would silently lose a tile.
	inventory, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "cluster_inventory",
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]float64{}
	for _, item := range inventory.Series {
		if len(item.Points) == 1 && item.Points[0].Value != nil {
			counts[item.Labels["resource"]] = *item.Points[0].Value
		}
	}
	for resource, want := range map[string]float64{
		"node": 1, "node_ready": 1, "pod_running": 1, "pod_pending": 0,
		"deployment": 1, "statefulset": 1, "daemonset": 1,
	} {
		if got, present := counts[resource]; !present || got != want {
			t.Fatalf("cluster_inventory %s = %v (present %t), want %v",
				resource, got, present, want)
		}
	}

	// The shortfall subtracts two unions that name their objects with different
	// labels. If the normalisation on either side is wrong the subtraction finds
	// no match and answers nothing at all, which looks exactly like a healthy
	// Cluster.
	shortfall, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "workload_replicas_unavailable",
		Start:     end.Add(-10 * time.Minute),
		End:       end,
		Step:      time.Minute,
		Top:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	missing := map[string]float64{}
	for _, item := range shortfall.Series {
		point := item.Points[len(item.Points)-1]
		if point.Value != nil {
			missing[item.Labels["workload_kind"]+"/"+item.Labels["workload"]] = *point.Value
		}
	}
	// 3 desired against 1 available, beside a StatefulSet and a DaemonSet that
	// are fully ready and must therefore report zero rather than disappear.
	if missing["Deployment/probe-app"] != 2 {
		t.Fatalf("workload_replicas_unavailable = %v, want Deployment/probe-app at 2", missing)
	}
	if _, present := missing["DaemonSet/probe-agent"]; !present {
		t.Fatalf("workload_replicas_unavailable dropped the ready DaemonSet: %v", missing)
	}

	// Pod density joins two families on the Node label. The Pod count comes from
	// one and the capacity from another, so a join that fails answers nothing.
	density, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "node_pod_utilization",
		Start:     end.Add(-10 * time.Minute),
		End:       end,
		Step:      time.Minute,
		Top:       10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(density.Series) != 1 {
		t.Fatalf("node_pod_utilization returned %d series, want the seeded Node", len(density.Series))
	}
	densityLast := density.Series[0].Points[len(density.Series[0].Points)-1]
	if densityLast.Value == nil || math.Abs(*densityLast.Value-1.0/110.0) > 0.0001 {
		t.Fatalf("node_pod_utilization last point = %+v, want 1/110", densityLast)
	}

	// The ratios whose denominator is guarded with `> 0`. A guard on the wrong
	// side of the join, or a join on a label only one side carries, removes the
	// series instead of answering wrongly — which the emptiness check above
	// already catches. What it cannot catch is a ratio that resolves to the
	// wrong pair, so each one is read for its value.
	//
	// 60 throttled periods per minute out of 600, a quarter of the quota's CPU
	// in use, and 4 GiB of a 10 GiB claim.
	for _, expected := range []struct {
		name  string
		value float64
	}{
		{"container_cpu_throttling", 0.1},
		{"namespace_quota_utilization", 0.25},
		{"pvc_utilization", 0.4},
	} {
		result, err := service.Query(context.Background(), Input{
			UserID:    userID,
			ClusterID: clusterID,
			Name:      expected.name,
			Start:     end.Add(-10 * time.Minute),
			End:       end,
			Step:      time.Minute,
			Top:       10,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Series) != 1 {
			t.Fatalf("%s returned %d series, want one", expected.name, len(result.Series))
		}
		points := result.Series[0].Points
		point := points[len(points)-1]
		if point.Value == nil || math.Abs(*point.Value-expected.value) > 0.001 {
			t.Fatalf("%s last point = %+v, want %v", expected.name, point, expected.value)
		}
	}

	// Collection health averages the collector's own scrape results. One of the
	// two seeded targets is down, so a Cluster that reports 1 here is one whose
	// failing targets are being averaged away rather than counted.
	health, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "collection_health",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(health.Series) != 1 || health.Series[0].Points[0].Value == nil ||
		*health.Series[0].Points[0].Value != 0.5 {
		t.Fatalf("collection_health = %+v, want one series at 0.5", health.Series)
	}

	// The container state charts read a reason the Agent's scrape filter keeps.
	// The two lists are shared, and a selector that drifted from the filter is
	// an empty chart for the fault an operator is most likely to be looking for.
	terminated, err := service.Query(context.Background(), Input{
		UserID:    userID,
		ClusterID: clusterID,
		Name:      "pod_container_terminated",
		Start:     end.Add(-10 * time.Minute),
		End:       end,
		Step:      time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(terminated.Series) != 1 || terminated.Series[0].Labels["reason"] != "OOMKilled" {
		t.Fatalf("pod_container_terminated = %+v, want the seeded OOMKilled series", terminated.Series)
	}
}

// waitForSeed polls one query until the seeded samples are queryable, and
// returns its answer so the caller can assert on it rather than ask twice.
//
// Polled rather than read once: VictoriaMetrics acknowledges a write before it
// serves it, and failing on the first miss would make every assertion below
// flaky for a reason that has nothing to do with ZKE. The Cluster memory gauge
// is the probe because it is written directly, without a join or a rate.
//
// The wait is for the newest point of the grid, not merely for the series to
// exist. The two are not the same moment: the older samples of a batch become
// queryable before the newest one, and a gate that stops at "something is
// there" lets the assertions below run against a grid whose last step is still
// empty — which they then report as a broken join rather than as a slow write.
func waitForSeed(t *testing.T, service *Service, clusterID string, end time.Time) Result {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := service.Query(context.Background(), Input{
			UserID:    userID,
			ClusterID: clusterID,
			Name:      "cluster_memory_usage",
			Start:     end.Add(-10 * time.Minute),
			End:       end,
			Step:      time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		if seedIsQueryable(result) || time.Now().After(deadline) {
			return result
		}
		time.Sleep(time.Second)
	}
}

func seedIsQueryable(result Result) bool {
	if len(result.Series) == 0 {
		return false
	}
	points := result.Series[0].Points
	return len(points) > 0 && points[len(points)-1].Value != nil
}

// seedKubeletSamples writes the metrics the generated scrape configuration
// collects, under the scope label the ingest gateway applies.
func seedKubeletSamples(t *testing.T, base string, clusterID string) {
	t.Helper()
	now := time.Now()
	var body []byte
	for offset := 10; offset >= 0; offset-- {
		at := now.Add(-time.Duration(offset) * time.Minute).UnixMilli()
		body = append(body, series(map[string]string{
			"__name__":                 "node_cpu_usage_seconds_total",
			"node":                     "probe-node",
			metricsingest.ClusterLabel: clusterID,
		}, float64(100-offset), at)...)
		body = append(body, series(map[string]string{
			"__name__":                 "node_memory_working_set_bytes",
			"node":                     "probe-node",
			metricsingest.ClusterLabel: clusterID,
		}, 1<<30, at)...)
		body = append(body, series(map[string]string{
			"__name__":                 "pod_cpu_usage_seconds_total",
			"namespace":                "kube-system",
			"pod":                      "probe-pod",
			metricsingest.ClusterLabel: clusterID,
		}, float64(50-offset), at)...)
		body = append(body, series(map[string]string{
			"__name__":                 "pod_memory_working_set_bytes",
			"namespace":                "kube-system",
			"pod":                      "probe-pod",
			metricsingest.ClusterLabel: clusterID,
		}, 1<<28, at)...)
		// The same endpoint reports the containers inside the Pod. A Pod is a
		// group of processes, and the container level is the only one that says
		// which of them is the one consuming.
		body = append(body, series(map[string]string{
			"__name__":                 "container_cpu_usage_seconds_total",
			"namespace":                "kube-system",
			"pod":                      "probe-pod",
			"container":                "app",
			metricsingest.ClusterLabel: clusterID,
		}, float64(40-offset), at)...)
		body = append(body, series(map[string]string{
			"__name__":                 "container_memory_working_set_bytes",
			"namespace":                "kube-system",
			"pod":                      "probe-pod",
			"container":                "app",
			metricsingest.ClusterLabel: clusterID,
		}, 1<<27, at)...)
		// The collector's own view of its targets. Two of them, one answering
		// and one not, because a Cluster where everything is up cannot tell a
		// working average from a constant.
		body = append(body, series(map[string]string{
			"__name__":                 "up",
			"job":                      "kubelet-resource",
			"node":                     "probe-node",
			metricsingest.ClusterLabel: clusterID,
		}, 1, at)...)
		body = append(body, series(map[string]string{
			"__name__":                 "up",
			"job":                      "node-exporter",
			"node":                     "probe-node",
			metricsingest.ClusterLabel: clusterID,
		}, 0, at)...)
		body = append(body, seedObjectSamples(clusterID, at)...)
		body = append(body, seedNodeSamples(clusterID, at, offset)...)
		body = append(body, seedCadvisorSamples(clusterID, at, offset)...)
		body = append(body, seedVolumeStatsSamples(clusterID, at)...)
	}
	response, err := http.Post(
		base+"/api/v1/write",
		"application/x-protobuf",
		bytes.NewReader(snappy.Encode(nil, body)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 {
		t.Fatalf("seed write status %d", response.StatusCode)
	}
}

// seedObjectSamples writes the kube-state-metrics families the utilisation,
// request and workload queries read.
//
// The Pod is owned by a ReplicaSet which is owned by a Deployment, because that
// is the two-level case the workload rollup exists for: a template that only
// handled the direct owner would pass a one-level fixture and report the
// ReplicaSet's generated name to every operator in production.
func seedObjectSamples(clusterID string, at int64) []byte {
	var body []byte
	for _, sample := range []struct {
		labels map[string]string
		value  float64
	}{
		{map[string]string{
			"__name__": "kube_node_status_allocatable",
			"node":     "probe-node", "resource": "cpu", "unit": "core",
		}, 4},
		{map[string]string{
			"__name__": "kube_node_status_allocatable",
			"node":     "probe-node", "resource": "memory", "unit": "byte",
		}, 8 << 30},
		{map[string]string{
			"__name__":  "kube_pod_container_resource_requests",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
			"node": "probe-node", "resource": "cpu", "unit": "core",
		}, 0.5},
		{map[string]string{
			"__name__":  "kube_pod_container_resource_requests",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
			"node": "probe-node", "resource": "memory", "unit": "byte",
		}, 512 << 20},
		{map[string]string{
			"__name__":  "kube_pod_container_resource_limits",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
			"node": "probe-node", "resource": "cpu", "unit": "core",
		}, 1},
		{map[string]string{
			"__name__":  "kube_pod_container_resource_limits",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
			"node": "probe-node", "resource": "memory", "unit": "byte",
		}, 1 << 30},
		{map[string]string{
			"__name__": "kube_pod_owner", "namespace": "kube-system",
			"pod": "probe-pod", "owner_kind": "ReplicaSet", "owner_name": "probe-rs",
		}, 1},
		{map[string]string{
			"__name__": "kube_replicaset_owner", "namespace": "kube-system",
			"replicaset": "probe-rs", "owner_kind": "Deployment", "owner_name": "probe-app",
		}, 1},
		{map[string]string{
			"__name__":  "kube_pod_container_status_restarts_total",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
		}, 3},
		{map[string]string{
			"__name__": "kube_node_status_capacity",
			"node":     "probe-node", "resource": "pods", "unit": "integer",
		}, 110},
		{map[string]string{
			"__name__": "kube_pod_info", "namespace": "kube-system",
			"pod": "probe-pod", "node": "probe-node",
		}, 1},
		{map[string]string{
			"__name__": "kube_node_status_condition",
			"node":     "probe-node", "condition": "Ready", "status": "true",
		}, 1},
		{map[string]string{
			"__name__": "kube_node_status_condition",
			"node":     "probe-node", "condition": "MemoryPressure", "status": "true",
		}, 0},
		{map[string]string{
			"__name__": "kube_pod_status_phase", "namespace": "kube-system",
			"pod": "probe-pod", "phase": "Running",
		}, 1},
		{map[string]string{
			"__name__": "kube_pod_status_phase", "namespace": "kube-system",
			"pod": "probe-pod", "phase": "Pending",
		}, 0},
		{map[string]string{
			"__name__":  "kube_deployment_status_replicas",
			"namespace": "kube-system", "deployment": "probe-app",
		}, 3},
		{map[string]string{
			"__name__":  "kube_deployment_status_replicas_available",
			"namespace": "kube-system", "deployment": "probe-app",
		}, 1},
		{map[string]string{
			"__name__":  "kube_statefulset_status_replicas",
			"namespace": "kube-system", "statefulset": "probe-set",
		}, 2},
		{map[string]string{
			"__name__":  "kube_statefulset_status_replicas_ready",
			"namespace": "kube-system", "statefulset": "probe-set",
		}, 2},
		{map[string]string{
			"__name__":  "kube_daemonset_status_desired_number_scheduled",
			"namespace": "kube-system", "daemonset": "probe-agent",
		}, 1},
		{map[string]string{
			"__name__":  "kube_daemonset_status_number_ready",
			"namespace": "kube-system", "daemonset": "probe-agent",
		}, 1},
		// A quota with room left, so the ratio has a denominator to divide by.
		// The `hard` side is guarded with `> 0` in the template, and a fixture
		// without it would drop the whole series rather than answer wrongly.
		{map[string]string{
			"__name__": "kube_resourcequota", "namespace": "kube-system",
			"resourcequota": "probe-quota", "resource": "requests.cpu", "type": "hard",
		}, 8},
		{map[string]string{
			"__name__": "kube_resourcequota", "namespace": "kube-system",
			"resourcequota": "probe-quota", "resource": "requests.cpu", "type": "used",
		}, 2},
		// The container state families. Both are filtered by reason at the
		// scrape, so the fixture uses reasons from that same shared list: one
		// the Agent keeps proves the selector and the filter still agree.
		{map[string]string{
			"__name__":  "kube_pod_container_status_waiting_reason",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
			"reason": "CrashLoopBackOff",
		}, 1},
		{map[string]string{
			"__name__":  "kube_pod_container_status_last_terminated_reason",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
			"reason": "OOMKilled",
		}, 1},
	} {
		sample.labels[metricsingest.ClusterLabel] = clusterID
		body = append(body, series(sample.labels, sample.value, at)...)
	}
	return body
}

// seedNodeSamples writes the node-exporter families the disk, network and
// saturation queries read.
//
// The offset is the sample's position in the seeded window. The counters the
// saturation queries read are rated against a guarded denominator, and a
// counter that never moves makes that guard drop the series entirely — the
// query then answers nothing while looking exactly like a healthy Node.
func seedNodeSamples(clusterID string, at int64, offset int) []byte {
	var body []byte
	for _, sample := range []struct {
		labels map[string]string
		value  float64
	}{
		{map[string]string{
			"__name__": "node_filesystem_avail_bytes",
			"node":     "probe-node", "mountpoint": "/", "device": "/dev/sda1",
		}, 40 << 30},
		{map[string]string{
			"__name__": "node_filesystem_size_bytes",
			"node":     "probe-node", "mountpoint": "/", "device": "/dev/sda1",
		}, 100 << 30},
		{map[string]string{
			"__name__": "node_network_receive_bytes_total",
			"node":     "probe-node", "device": "eth0",
		}, 1_000_000},
		{map[string]string{
			"__name__": "node_network_transmit_bytes_total",
			"node":     "probe-node", "device": "eth0",
		}, 2_000_000},
		{map[string]string{
			"__name__": "node_disk_read_bytes_total",
			"node":     "probe-node", "device": "sda",
		}, 3_000_000},
		{map[string]string{
			"__name__": "node_disk_written_bytes_total",
			"node":     "probe-node", "device": "sda",
		}, 4_000_000},
		{map[string]string{
			"__name__": "node_filesystem_files",
			"node":     "probe-node", "mountpoint": "/", "device": "/dev/sda1",
		}, 1_000_000},
		{map[string]string{
			"__name__": "node_filesystem_files_free",
			"node":     "probe-node", "mountpoint": "/", "device": "/dev/sda1",
		}, 900_000},
		{map[string]string{
			"__name__": "node_cpu_seconds_total",
			"node":     "probe-node", "cpu": "0", "mode": "idle",
		}, 900_000},
		{map[string]string{
			"__name__": "node_cpu_seconds_total",
			"node":     "probe-node", "cpu": "0", "mode": "iowait",
		}, 1_000},
		{map[string]string{
			"__name__": "node_memory_MemAvailable_bytes", "node": "probe-node",
		}, 6 << 30},
		{map[string]string{"__name__": "node_load1", "node": "probe-node"}, 1.5},
		{map[string]string{
			"__name__": "node_disk_reads_completed_total",
			"node":     "probe-node", "device": "sda",
		}, 10_000},
		{map[string]string{
			"__name__": "node_disk_writes_completed_total",
			"node":     "probe-node", "device": "sda",
		}, 20_000},
		{map[string]string{
			"__name__": "node_disk_io_time_seconds_total",
			"node":     "probe-node", "device": "sda",
		}, 500},
		{map[string]string{
			"__name__": "node_network_receive_errs_total",
			"node":     "probe-node", "device": "eth0",
		}, 1},
		{map[string]string{
			"__name__": "node_network_transmit_errs_total",
			"node":     "probe-node", "device": "eth0",
		}, 2},
		{map[string]string{
			"__name__": "node_network_receive_drop_total",
			"node":     "probe-node", "device": "eth0",
		}, 3},
		{map[string]string{
			"__name__": "node_network_transmit_drop_total",
			"node":     "probe-node", "device": "eth0",
		}, 4},
		// Conntrack is a pair of gauges rather than a counter: the table's
		// occupancy against the table's size.
		{map[string]string{
			"__name__": "node_nf_conntrack_entries", "node": "probe-node",
		}, 16_384},
		{map[string]string{
			"__name__": "node_nf_conntrack_entries_limit", "node": "probe-node",
		}, 131_072},
	} {
		sample.labels[metricsingest.ClusterLabel] = clusterID
		body = append(body, series(sample.labels, sample.value, at)...)
	}
	// The counters, which have to advance across the window to survive their
	// own rate. The netstat names are the four the exporter is configured to
	// expose; the pressure names need a kernel with /proc/pressure, which is
	// the one family a real Node may legitimately answer nothing for.
	for _, sample := range []struct {
		labels map[string]string
		base   float64
		step   float64
	}{
		{map[string]string{
			"__name__": "node_netstat_Tcp_OutSegs", "node": "probe-node",
		}, 1_000_000, 10_000},
		{map[string]string{
			"__name__": "node_netstat_Tcp_RetransSegs", "node": "probe-node",
		}, 10_000, 100},
		{map[string]string{
			"__name__": "node_netstat_TcpExt_ListenDrops", "node": "probe-node",
		}, 100, 6},
		{map[string]string{
			"__name__": "node_netstat_TcpExt_ListenOverflows", "node": "probe-node",
		}, 50, 6},
		{map[string]string{
			"__name__": "node_pressure_cpu_waiting_seconds_total", "node": "probe-node",
		}, 100, 3},
		{map[string]string{
			"__name__": "node_pressure_memory_waiting_seconds_total", "node": "probe-node",
		}, 50, 1.5},
		{map[string]string{
			"__name__": "node_pressure_io_waiting_seconds_total", "node": "probe-node",
		}, 20, 0.6},
	} {
		sample.labels[metricsingest.ClusterLabel] = clusterID
		body = append(body, series(
			sample.labels,
			sample.base+sample.step*float64(10-offset),
			at,
		)...)
	}
	return body
}

// seedCadvisorSamples writes the families the kubelet's cAdvisor endpoint
// contributes: CPU throttling and per-Pod network.
//
// Throttling is a ratio of two counters, and the denominator carries a `> 0`
// guard because a container with no CPU limit reports no periods at all. Both
// therefore have to advance, or the guard removes the series and the chart is
// empty for a container that is in fact being throttled.
//
// The network families are reported on the Pod's own cgroup, where cAdvisor
// leaves `container` empty — which is why the template selects on `pod` rather
// than filtering the container out.
func seedCadvisorSamples(clusterID string, at int64, offset int) []byte {
	var body []byte
	for _, sample := range []struct {
		labels map[string]string
		base   float64
		step   float64
	}{
		{map[string]string{
			"__name__":  "container_cpu_cfs_periods_total",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
		}, 10_000, 600},
		{map[string]string{
			"__name__":  "container_cpu_cfs_throttled_periods_total",
			"namespace": "kube-system", "pod": "probe-pod", "container": "app",
		}, 1_000, 60},
		{map[string]string{
			"__name__":  "container_network_receive_bytes_total",
			"namespace": "kube-system", "pod": "probe-pod", "container": "",
			"interface": "eth0",
		}, 5_000_000, 60_000},
		{map[string]string{
			"__name__":  "container_network_transmit_bytes_total",
			"namespace": "kube-system", "pod": "probe-pod", "container": "",
			"interface": "eth0",
		}, 6_000_000, 120_000},
	} {
		sample.labels[metricsingest.ClusterLabel] = clusterID
		body = append(body, series(
			sample.labels,
			sample.base+sample.step*float64(10-offset),
			at,
		)...)
	}
	return body
}

// seedVolumeStatsSamples writes the kubelet's volume statistics, the only
// place a PersistentVolumeClaim's fullness is reported. kube-state knows the
// claim exists and how large it was asked for; neither number says whether it
// is about to fill up.
func seedVolumeStatsSamples(clusterID string, at int64) []byte {
	var body []byte
	for _, sample := range []struct {
		labels map[string]string
		value  float64
	}{
		{map[string]string{
			"__name__":  "kubelet_volume_stats_capacity_bytes",
			"namespace": "kube-system", "persistentvolumeclaim": "probe-claim",
		}, 10 << 30},
		{map[string]string{
			"__name__":  "kubelet_volume_stats_used_bytes",
			"namespace": "kube-system", "persistentvolumeclaim": "probe-claim",
		}, 4 << 30},
		{map[string]string{
			"__name__":  "kubelet_volume_stats_inodes",
			"namespace": "kube-system", "persistentvolumeclaim": "probe-claim",
		}, 1_000_000},
		{map[string]string{
			"__name__":  "kubelet_volume_stats_inodes_used",
			"namespace": "kube-system", "persistentvolumeclaim": "probe-claim",
		}, 250_000},
	} {
		sample.labels[metricsingest.ClusterLabel] = clusterID
		body = append(body, series(sample.labels, sample.value, at)...)
	}
	return body
}

func series(labels map[string]string, value float64, timestampMS int64) []byte {
	encoded := []byte{}
	for name, labelValue := range labels {
		label := protowire.AppendTag(nil, 1, protowire.BytesType)
		label = protowire.AppendString(label, name)
		label = protowire.AppendTag(label, 2, protowire.BytesType)
		label = protowire.AppendString(label, labelValue)
		encoded = protowire.AppendTag(encoded, 1, protowire.BytesType)
		encoded = protowire.AppendBytes(encoded, label)
	}
	sample := protowire.AppendTag(nil, 1, protowire.Fixed64Type)
	sample = protowire.AppendFixed64(sample, math.Float64bits(value))
	sample = protowire.AppendTag(sample, 2, protowire.VarintType)
	sample = protowire.AppendVarint(sample, uint64(timestampMS))
	encoded = protowire.AppendTag(encoded, 2, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, sample)

	request := protowire.AppendTag(nil, 1, protowire.BytesType)
	return protowire.AppendBytes(request, encoded)
}
