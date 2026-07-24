package pki

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/store/migrations"
)

func TestEnsurePinsManagedPKIAndRejectsLostPV(t *testing.T) {
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	var randomValue [8]byte
	if _, err := rand.Read(randomValue[:]); err != nil {
		t.Fatal(err)
	}
	schemaName := "zke_pki_test_" + hex.EncodeToString(randomValue[:])
	quotedSchema := pgx.Identifier{schemaName}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quotedSchema); err != nil {
		adminPool.Close()
		t.Fatal(err)
	}
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	poolConfig.ConnConfig.RuntimeParams["search_path"] = schemaName
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := adminPool.Exec(
			context.Background(),
			"DROP SCHEMA "+quotedSchema+" CASCADE",
		); err != nil {
			t.Errorf("drop integration test schema: %v", err)
		}
		adminPool.Close()
	})
	if _, err := migrations.Apply(ctx, pool); err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	config := Config{
		Directory:                directory,
		AutoGenerate:             true,
		AgentClientCAValidity:    10 * 365 * 24 * time.Hour,
		AgentListenerCAValidity:  20 * 365 * 24 * time.Hour,
		AgentListenerValidity:    10 * 365 * 24 * time.Hour,
		AgentListenerRenewBefore: 365 * 24 * time.Hour,
		ListenerDNSNames:         []string{"localhost"},
	}
	first, err := Ensure(ctx, pool, config, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	second, err := Ensure(ctx, pool, config, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if first.State.AgentClientCAFingerprint !=
		second.State.AgentClientCAFingerprint {
		t.Fatal("repeated Ensure changed the Agent Client CA")
	}
	for _, name := range allFileNames {
		if err := os.Remove(directory + string(os.PathSeparator) + name); err != nil {
			t.Fatal(err)
		}
	}
	_, err = Ensure(ctx, pool, config, time.Now().UTC())
	if err == nil ||
		!strings.Contains(err.Error(), "database state exists") {
		t.Fatalf("Ensure() after PV loss error = %v, want fail-closed error", err)
	}
}
