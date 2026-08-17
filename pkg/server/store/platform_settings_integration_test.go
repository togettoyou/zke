package store_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

// The settings row records who changed it, so an update needs an actor even
// when the test is about something else.
const testActorUserID = "11111111-1111-4111-8111-111111111111"

// The workloads arrive as one JSON object built by the database, so a column
// renamed in a migration and not in the struct tags silently drops a value
// instead of failing to compile. Only a real database can catch that; a fake
// store decodes nothing.
//
// The defaults are asserted rather than just the shape: they name real
// published images at pinned versions, and an unpinned or misspelled one only
// fails much later, inside somebody else's Cluster.
func TestPlatformSettingsDefaultsCoverEveryWorkload(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settings, err := store.NewPlatformSettingsStore(pool).GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The node exporter runs on every Node, so its budget is multiplied by the
	// size of the Cluster and is deliberately the smallest of the five.
	//
	// The Agent's requests are asserted because their absence is what would make
	// it a BestEffort Pod — the first thing evicted under Node memory pressure,
	// and the one whose loss takes the Cluster out of ZKE. Its empty CPU limit
	// is asserted for the same reason it is empty: a throttled Agent does not
	// fail, it just makes everything in that Cluster slow.
	want := map[string]store.WorkloadSettings{
		"agent": {
			Image: "ghcr.io/togettoyou/zke-agent:latest", ImagePullPolicy: "IfNotPresent",
			CPURequest: "50m", MemoryRequest: "128Mi", CPULimit: "", MemoryLimit: "512Mi",
		},
		"cluster-terminal": {
			Image: "ghcr.io/togettoyou/zke-agent:latest", ImagePullPolicy: "IfNotPresent",
			CPURequest: "25m", MemoryRequest: "64Mi", CPULimit: "500m", MemoryLimit: "256Mi",
		},
		"collector": {
			Image: "victoriametrics/vmagent:v1.149.0", ImagePullPolicy: "IfNotPresent",
			CPURequest: "50m", MemoryRequest: "128Mi", CPULimit: "500m", MemoryLimit: "512Mi",
		},
		"kube-state-metrics": {
			Image:           "registry.k8s.io/kube-state-metrics/kube-state-metrics:v2.19.1",
			ImagePullPolicy: "IfNotPresent",
			CPURequest:      "20m", MemoryRequest: "128Mi", CPULimit: "500m", MemoryLimit: "512Mi",
		},
		"node-exporter": {
			Image: "quay.io/prometheus/node-exporter:v1.12.1", ImagePullPolicy: "IfNotPresent",
			CPURequest: "10m", MemoryRequest: "32Mi", CPULimit: "200m", MemoryLimit: "128Mi",
		},
	}
	if !reflect.DeepEqual(settings.Workloads, want) {
		t.Fatalf("workload defaults = %+v, want %+v", settings.Workloads, want)
	}
	if settings.ClusterTerminalSessionTTL != 15*time.Minute || settings.Revision != 1 {
		t.Fatalf(
			"session lifetime = %s, revision = %d",
			settings.ClusterTerminalSessionTTL,
			settings.Revision,
		)
	}
}

// One revision covers the settings row and every workload row, so a save that
// names one workload has to move the revision the next save is checked against
// — and must not touch a workload it did not name.
func TestPlatformSettingsUpdateIsPartialAndBumpsOneRevision(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settingsStore := store.NewPlatformSettingsStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)

	updated, err := settingsStore.UpdateSettings(ctx, store.UpdatePlatformSettingsParams{
		Workloads: map[string]store.WorkloadSettings{
			"collector": {
				Image: "registry.example.com/vmagent:v2", ImagePullPolicy: "Always",
				CPURequest: "60m", MemoryRequest: "256Mi",
			},
		},
		ExpectedRevision: 1, ActorUserID: testActorUserID, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Revision != 2 {
		t.Fatalf("revision = %d, want 2", updated.Revision)
	}
	collector := updated.Workloads["collector"]
	if collector.Image != "registry.example.com/vmagent:v2" ||
		collector.CPULimit != "" || collector.MemoryLimit != "" {
		t.Fatalf("collector = %+v", collector)
	}
	if updated.Workloads["node-exporter"].Image != "quay.io/prometheus/node-exporter:v1.12.1" {
		t.Fatalf("unnamed workload was rewritten: %+v", updated.Workloads["node-exporter"])
	}
	if updated.ClusterTerminalSessionTTL != 15*time.Minute {
		t.Fatalf("session lifetime = %s, want it untouched", updated.ClusterTerminalSessionTTL)
	}

	// The revision the caller held is now stale, so the same save again is a
	// conflict rather than a second write.
	_, err = settingsStore.UpdateSettings(ctx, store.UpdatePlatformSettingsParams{
		Workloads: map[string]store.WorkloadSettings{
			"collector": {Image: "registry.example.com/vmagent:v3", ImagePullPolicy: "Always"},
		},
		ExpectedRevision: 1, ActorUserID: testActorUserID, Now: now,
	})
	if !errors.Is(err, store.ErrPlatformSettingsConflict) {
		t.Fatalf("stale update error = %v, want ErrPlatformSettingsConflict", err)
	}

	// A name no row carries must fail rather than write nothing and report
	// success: it means the migrations and the Server's registry have drifted.
	_, err = settingsStore.UpdateSettings(ctx, store.UpdatePlatformSettingsParams{
		Workloads: map[string]store.WorkloadSettings{
			"log-collector": {Image: "registry.example.com/vector:v1", ImagePullPolicy: "Always"},
		},
		ExpectedRevision: 2, ActorUserID: testActorUserID, Now: now,
	})
	if !errors.Is(err, store.ErrPlatformWorkloadNotFound) {
		t.Fatalf("unknown workload error = %v, want ErrPlatformWorkloadNotFound", err)
	}
	current, err := settingsStore.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Revision != 2 {
		t.Fatalf("revision after refused update = %d, want 2", current.Revision)
	}
}

func TestPlatformDefaultEndpointReconciliationIsIdempotent(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settingsStore := store.NewPlatformSettingsStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	input := store.ReconcileDefaultEndpointParams{
		ReservedID: "00000000-0000-0000-0000-000000000012", ReservedName: "部署配置默认端点",
		PresetProfileIDs: []string{
			"00000000-0000-0000-0000-000000000010",
			"00000000-0000-0000-0000-000000000011",
		},
		RegistrationURL: "https://zke.example.com", QUICAddress: "zke.example.com:8443", Now: now,
	}

	first, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := settingsStore.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != input.ReservedID || settings.DefaultEndpointProfileID != first.ID ||
		first.RegistrationURL != input.RegistrationURL || first.QUICAddress != input.QUICAddress {
		t.Fatalf("unexpected reconciled endpoint: profile=%+v settings=%+v", first, settings)
	}

	second, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	settingsAgain, err := settingsStore.GetSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision != first.Revision || settingsAgain.Revision != settings.Revision {
		t.Fatalf("idempotent reconciliation changed revisions: profile %d -> %d, settings %d -> %d",
			first.Revision, second.Revision, settings.Revision, settingsAgain.Revision)
	}

	input.RegistrationURL = "http://zke.internal:8080"
	input.QUICAddress = "zke.internal:8443"
	input.Now = now.Add(time.Minute)
	changed, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID != first.ID || changed.Revision != first.Revision+1 ||
		changed.RegistrationURL != input.RegistrationURL || changed.QUICAddress != input.QUICAddress {
		t.Fatalf("changed deployment endpoint was not synchronized: %+v", changed)
	}
}

func TestPlatformDefaultEndpointReconciliationSwitchesCleanlyBetweenDeploymentAndPresets(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settingsStore := store.NewPlatformSettingsStore(pool)
	now := time.Now().UTC().Truncate(time.Microsecond)
	input := store.ReconcileDefaultEndpointParams{
		ReservedID: "00000000-0000-0000-0000-000000000012", ReservedName: "部署配置默认端点",
		PresetProfileIDs: []string{
			"00000000-0000-0000-0000-000000000010",
			"00000000-0000-0000-0000-000000000011",
		},
		RegistrationURL: "https://zke.example.com", QUICAddress: "zke.example.com:8443", Now: now,
	}

	custom, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if custom.ID != input.ReservedID {
		t.Fatalf("custom profile ID = %q, want reserved ID", custom.ID)
	}

	input.RegistrationURL = "http://127.0.0.1:8080"
	input.QUICAddress = "127.0.0.1:8443"
	input.Now = now.Add(time.Minute)
	loopback, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if loopback.ID != input.PresetProfileIDs[0] {
		t.Fatalf("loopback profile ID = %q, want %q", loopback.ID, input.PresetProfileIDs[0])
	}
	if _, err := settingsStore.GetEndpointProfile(ctx, input.ReservedID); err != store.ErrEndpointProfileNotFound {
		t.Fatalf("superseded deployment profile error = %v, want not found", err)
	}

	input.RegistrationURL = "https://zke.example.com"
	input.QUICAddress = "zke.example.com:8443"
	input.Now = now.Add(2 * time.Minute)
	recreated, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if recreated.ID != input.ReservedID || recreated.Revision != 1 {
		t.Fatalf("recreated deployment profile = %+v", recreated)
	}

	input.RegistrationURL = "http://host.docker.internal:8080"
	input.QUICAddress = "host.docker.internal:8443"
	input.Now = now.Add(3 * time.Minute)
	desktop, err := settingsStore.ReconcileDefaultEndpoint(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	if desktop.ID != input.PresetProfileIDs[1] {
		t.Fatalf("desktop profile ID = %q, want %q", desktop.ID, input.PresetProfileIDs[1])
	}
	if _, err := settingsStore.GetEndpointProfile(ctx, input.ReservedID); err != store.ErrEndpointProfileNotFound {
		t.Fatalf("superseded deployment profile error = %v, want not found", err)
	}
}
