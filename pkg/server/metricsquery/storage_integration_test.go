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
	// Every range query in the catalogue, so a template that compiles here but
	// is rejected by the real engine cannot ship. `top` is set where the query
	// demands it; the rest are asked unbounded, which is how the Console asks
	// for Cluster totals.
	for _, testCase := range []struct {
		name string
		top  int
	}{
		{name: "cluster_cpu_usage"},
		{name: "cluster_memory_usage"},
		{name: "node_cpu_usage"},
		{name: "node_memory_usage"},
		{name: "namespace_cpu_usage"},
		{name: "namespace_memory_usage"},
		{name: "pod_cpu_usage", top: 10},
		{name: "pod_memory_usage", top: 10},
		{name: "cluster_cpu_utilization"},
		{name: "cluster_memory_utilization"},
		{name: "node_cpu_utilization"},
		{name: "node_memory_utilization"},
		{name: "namespace_cpu_requests"},
		{name: "namespace_memory_requests"},
		{name: "namespace_cpu_limits"},
		{name: "namespace_memory_limits"},
		{name: "workload_cpu_usage", top: 10},
		{name: "workload_memory_usage", top: 10},
		{name: "pod_restarts", top: 10},
		{name: "node_filesystem_utilization"},
		{name: "node_network_receive"},
		{name: "node_network_transmit"},
		{name: "node_disk_read"},
		{name: "node_disk_write"},
	} {
		name := testCase.name
		t.Run(name, func(t *testing.T) {
			result, err := service.Query(context.Background(), Input{
				UserID: userID,
				Name:   name,
				Start:  end.Add(-10 * time.Minute),
				End:    end,
				Step:   time.Minute,
				Top:    testCase.top,
			})
			if err != nil {
				t.Fatalf("%s failed against storage: %v", name, err)
			}
			// The grid is produced by this Server, so it must be complete even
			// where storage returned nothing.
			for _, series := range result.Series {
				if len(series.Points) != 11 {
					t.Fatalf("%s returned %d points, want the full grid", name, len(series.Points))
				}
			}
		})
	}

	t.Run("collection_health", func(t *testing.T) {
		result, err := service.Query(context.Background(), Input{
			UserID: userID,
			Name:   "collection_health",
		})
		if err != nil {
			t.Fatalf("collection_health failed against storage: %v", err)
		}
		if result.Kind != KindInstant {
			t.Fatalf("collection_health kind = %s", result.Kind)
		}
	})

	// The memory query reads a gauge that was just written, so it is the one
	// that must come back with data. A template that compiles but selects
	// nothing would otherwise pass unnoticed.
	//
	// Polled rather than read once: VictoriaMetrics accepts a write before it
	// is queryable, and failing on the first miss would make this test flaky
	// for a reason that has nothing to do with ZKE.
	var memory Result
	deadline := time.Now().Add(30 * time.Second)
	for {
		result, err := service.Query(context.Background(), Input{
			UserID: userID,
			Name:   "cluster_memory_usage",
			Start:  end.Add(-10 * time.Minute),
			End:    end,
			Step:   time.Minute,
		})
		if err != nil {
			t.Fatal(err)
		}
		memory = result
		if len(memory.Series) > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
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
		UserID: userID,
		Name:   "workload_memory_usage",
		Start:  end.Add(-10 * time.Minute),
		End:    end,
		Step:   time.Minute,
		Top:    10,
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
		UserID: userID,
		Name:   "node_memory_utilization",
		Start:  end.Add(-10 * time.Minute),
		End:    end,
		Step:   time.Minute,
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
		body = append(body, seedObjectSamples(clusterID, at)...)
		body = append(body, seedNodeSamples(clusterID, at)...)
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
	} {
		sample.labels[metricsingest.ClusterLabel] = clusterID
		body = append(body, series(sample.labels, sample.value, at)...)
	}
	return body
}

// seedNodeSamples writes the node-exporter families the disk and network
// queries read.
func seedNodeSamples(clusterID string, at int64) []byte {
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
