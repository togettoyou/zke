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
	loaded, err := loadAndValidate(paths, now)
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
	loaded, err := loadAndValidate(paths, now)
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

func TestRenewListenerAddsConfiguredSAN(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	paths := managedPaths(directory)
	now := time.Now().UTC().Truncate(time.Second)
	config := Config{
		Directory:             directory,
		AgentClientCAValidity: 10 * 365 * 24 * time.Hour, AgentListenerCAValidity: 20 * 365 * 24 * time.Hour,
		AgentListenerValidity: 10 * 365 * 24 * time.Hour, AgentListenerRenewBefore: 365 * 24 * time.Hour,
		ListenerDNSNames: []string{"localhost"},
	}
	if err := generateAll(paths, config, now); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadAndValidate(paths, now)
	if err != nil {
		t.Fatal(err)
	}
	config.ListenerDNSNames = append(config.ListenerDNSNames, "host.docker.internal")
	if listenerMatchesConfig(loaded.listenerCertificate, config) {
		t.Fatal("original Listener certificate unexpectedly covers the new SAN")
	}
	if err := renewListener(paths, config, loaded, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	renewed, err := loadAndValidate(paths, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !listenerMatchesConfig(renewed.listenerCertificate, config) {
		t.Fatal("renewed Listener certificate does not cover configured SANs")
	}
	config.ListenerDNSNames = []string{"localhost"}
	if listenerMatchesConfig(renewed.listenerCertificate, config) {
		t.Fatal("Listener certificate with a superseded SAN unexpectedly matched configuration")
	}
	if err := renewListener(paths, config, renewed, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	cleaned, err := loadAndValidate(paths, now.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if !listenerMatchesConfig(cleaned.listenerCertificate, config) ||
		len(cleaned.listenerCertificate.DNSNames) != 1 {
		t.Fatalf("superseded Listener SAN was not removed: %v", cleaned.listenerCertificate.DNSNames)
	}
}
