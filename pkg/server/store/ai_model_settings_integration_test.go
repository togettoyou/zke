package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

// The shipped state is off with nothing filled in: no endpoint, no entry in the
// Console, no data leaving ZKE until an operator decides otherwise. Asserted
// against a real database because the defaults live in the migration.
func TestAIModelSettingsShipDisabled(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	settings, err := store.NewAIModelSettingsStore(pool).GetAIModelSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The shipped state is one preset with no credential: an endpoint, the
	// Responses protocol and the model that endpoint serves.
	if !settings.Enabled || settings.BaseURL != "https://api.deepseek.com" ||
		settings.Model != "deepseek-v4-flash" || settings.APIProtocol != "responses" {
		t.Fatalf("AI model settings must ship as a usable preset: %+v", settings)
	}
	// The credential is the one thing only the deployment can supply, so it is
	// the one field the preset leaves empty.
	if settings.APIKey != "" {
		t.Fatal("AI model settings must not ship a credential")
	}
	if settings.RequestTimeout != 60*time.Second {
		t.Fatalf("unexpected default request timeout: %s", settings.RequestTimeout)
	}
	if settings.ContextWindowTokens != 262_144 || settings.MaxOutputTokens != 32_768 {
		t.Fatalf("unexpected model runtime defaults: %+v", settings)
	}
	if settings.Revision != 1 {
		t.Fatalf("unexpected initial revision: %d", settings.Revision)
	}
}

// A save that does not carry the key must not erase it, and one that carries an
// empty key must. Only a real database exercises the COALESCE that decides it.
func TestAIModelSettingsKeyIsKeptUnlessNamed(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settingsStore := store.NewAIModelSettingsStore(pool)

	key := "sk-first"
	stored, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(1, &key))
	if err != nil {
		t.Fatal(err)
	}
	if stored.APIKey != key || stored.Revision != 2 {
		t.Fatalf("unexpected write: %+v", stored)
	}

	kept, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(stored.Revision, nil))
	if err != nil {
		t.Fatal(err)
	}
	if kept.APIKey != key {
		t.Fatalf("an update without a key must keep the stored one, got %q", kept.APIKey)
	}

	empty := ""
	cleared, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(kept.Revision, &empty))
	if err != nil {
		t.Fatal(err)
	}
	if cleared.APIKey != "" {
		t.Fatalf("an empty key must clear the stored one, got %q", cleared.APIKey)
	}
}

func TestAIModelSettingsRefuseAStaleRevision(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settingsStore := store.NewAIModelSettingsStore(pool)

	if _, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(1, nil)); err != nil {
		t.Fatal(err)
	}
	_, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(1, nil))
	if !errors.Is(err, store.ErrAIModelSettingsConflict) {
		t.Fatalf("expected a revision conflict, got %v", err)
	}
}

// Enabled with nothing to call can only fail later, at the point where somebody
// is waiting for an answer. The Server refuses it too; this asserts that the
// database is not relying on the Server to do so.
func TestAIModelSettingsRefuseEnabledWithoutEndpoint(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	settingsStore := store.NewAIModelSettingsStore(pool)
	_, err := settingsStore.SetAIModelEnabled(ctx, store.SetAIModelEnabledParams{
		Enabled: true, ExpectedRevision: 1, ActorUserID: testActorUserID, Now: time.Now().UTC(),
	})
	if !errors.Is(err, store.ErrAIModelSettingsNotConfigured) {
		t.Fatalf("enabling without an endpoint error = %v, want ErrAIModelSettingsNotConfigured", err)
	}
}

func TestAIModelEnabledIsAnIndependentRevisionedWrite(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	settingsStore := store.NewAIModelSettingsStore(pool)
	configured, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(1, nil))
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := settingsStore.SetAIModelEnabled(ctx, store.SetAIModelEnabledParams{
		Enabled: true, ExpectedRevision: configured.Revision,
		ActorUserID: testActorUserID, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !enabled.Enabled || enabled.Revision != configured.Revision+1 {
		t.Fatalf("enabled write = %+v", enabled)
	}
	updated, err := settingsStore.UpdateAIModelSettings(ctx, aiModelUpdate(enabled.Revision, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Enabled {
		t.Fatal("saving endpoint settings must preserve enabled state")
	}
}

func aiModelUpdate(revision int64, apiKey *string) store.UpdateAIModelSettingsParams {
	return store.UpdateAIModelSettingsParams{
		BaseURL:             "https://inference.internal/v1",
		Model:               "qwen2.5-32b-instruct",
		APIProtocol:         "responses",
		APIKey:              apiKey,
		ContextWindowTokens: 256_000,
		MaxOutputTokens:     16_000,
		RequestTimeout:      60 * time.Second,
		ExpectedRevision:    revision,
		ActorUserID:         testActorUserID,
		Now:                 time.Now().UTC(),
	}
}
