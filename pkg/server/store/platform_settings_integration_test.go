package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

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
