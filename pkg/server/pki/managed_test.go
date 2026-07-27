package pki

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestGenerateAndValidateManagedMaterial(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	paths := managedPaths(directory)
	now := time.Now().UTC().Truncate(time.Second)
	config := Config{
		Directory:                directory,
		AutoGenerate:             true,
		AgentClientCAValidity:    10 * 365 * 24 * time.Hour,
		AgentListenerCAValidity:  20 * 365 * 24 * time.Hour,
		AgentListenerValidity:    10 * 365 * 24 * time.Hour,
		AgentListenerRenewBefore: 365 * 24 * time.Hour,
		ListenerDNSNames:         []string{"localhost"},
		ListenerIPAddresses:      []string{"127.0.0.1"},
	}
	if err := generateAll(paths, config, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAndValidate(paths, config, now)
	if err != nil {
		t.Fatal(err)
	}
	state := stateFromMaterial(loaded)
	if len(state.AgentClientCAFingerprint) != 64 ||
		len(state.AgentListenerCAFingerprint) != 64 ||
		len(state.AgentListenerCertificateFingerprint) != 64 {
		t.Fatalf("unexpected certificate fingerprints: %+v", state)
	}
	if runtime.GOOS != "windows" {
		for _, path := range []string{
			paths.AgentClientCAPrivateKey,
			paths.AgentListenerCAPrivateKey,
			paths.AgentListenerPrivateKey,
		} {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 {
				t.Errorf("%s permissions = %o, want 600", path, info.Mode().Perm())
			}
		}
	}
}

func TestRenewListenerKeepsPrivateKey(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	paths := managedPaths(directory)
	now := time.Now().UTC().Truncate(time.Second)
	config := Config{
		Directory:                directory,
		AutoGenerate:             true,
		AgentClientCAValidity:    10 * 365 * 24 * time.Hour,
		AgentListenerCAValidity:  20 * 365 * 24 * time.Hour,
		AgentListenerValidity:    10 * 365 * 24 * time.Hour,
		AgentListenerRenewBefore: 365 * 24 * time.Hour,
		ListenerDNSNames:         []string{"localhost"},
	}
	if err := generateAll(paths, config, now); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.AgentListenerPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAndValidate(paths, config, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := renewListener(paths, config, loaded, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(paths.AgentListenerPrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("Agent Listener renewal changed the private key")
	}
}
