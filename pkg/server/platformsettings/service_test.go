package platformsettings

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
)

const testUserID = "11111111-1111-4111-8111-111111111111"
const testProfileID = "22222222-2222-4222-8222-222222222222"

type memoryStore struct {
	settings  store.PlatformSettings
	profiles  map[string]store.AgentEndpointProfile
	updateErr error
}

func (memory *memoryStore) ListEndpointProfiles(context.Context) ([]store.AgentEndpointProfile, error) {
	result := make([]store.AgentEndpointProfile, 0, len(memory.profiles))
	for _, profile := range memory.profiles {
		result = append(result, profile)
	}
	return result, nil
}
func (memory *memoryStore) GetEndpointProfile(_ context.Context, id string) (store.AgentEndpointProfile, error) {
	profile, ok := memory.profiles[id]
	if !ok {
		return store.AgentEndpointProfile{}, store.ErrEndpointProfileNotFound
	}
	return profile, nil
}
func (memory *memoryStore) CreateEndpointProfile(_ context.Context, input store.CreateAgentEndpointProfileParams) (store.AgentEndpointProfile, error) {
	profile := store.AgentEndpointProfile{ID: input.ID, Name: input.Name, RegistrationURL: input.RegistrationURL, QUICAddress: input.QUICAddress, RegistrationCACertificatePEM: input.RegistrationCACertificatePEM, Enabled: input.Enabled, Revision: 1, CreatedAt: input.Now, UpdatedAt: input.Now}
	memory.profiles[profile.ID] = profile
	return profile, nil
}
func (memory *memoryStore) UpdateEndpointProfile(_ context.Context, input store.UpdateAgentEndpointProfileParams) (store.AgentEndpointProfile, error) {
	if memory.updateErr != nil {
		return store.AgentEndpointProfile{}, memory.updateErr
	}
	profile, ok := memory.profiles[input.ID]
	if !ok {
		return store.AgentEndpointProfile{}, store.ErrEndpointProfileNotFound
	}
	if profile.Revision != input.ExpectedRevision {
		return store.AgentEndpointProfile{}, store.ErrEndpointProfileConflict
	}
	profile.Name, profile.RegistrationURL, profile.QUICAddress = input.Name, input.RegistrationURL, input.QUICAddress
	profile.RegistrationCACertificatePEM, profile.Enabled = input.RegistrationCACertificatePEM, input.Enabled
	profile.Revision++
	profile.UpdatedAt = input.Now
	memory.profiles[input.ID] = profile
	return profile, nil
}
func (memory *memoryStore) DeleteEndpointProfile(_ context.Context, id string) error {
	delete(memory.profiles, id)
	return nil
}
func (memory *memoryStore) GetSettings(context.Context) (store.PlatformSettings, error) {
	return memory.settings, nil
}
func (memory *memoryStore) UpdateSettings(_ context.Context, input store.UpdatePlatformSettingsParams) (store.PlatformSettings, error) {
	memory.settings = store.PlatformSettings{DefaultEndpointProfileID: memory.settings.DefaultEndpointProfileID, AgentImage: input.AgentImage, AgentImagePullPolicy: input.AgentImagePullPolicy, ClusterTerminalImage: input.ClusterTerminalImage, ClusterTerminalImagePullPolicy: input.ClusterTerminalImagePullPolicy, ClusterTerminalSessionTTL: input.ClusterTerminalSessionTTL, Revision: input.ExpectedRevision + 1, UpdatedAt: input.Now}
	return memory.settings, nil
}

func TestPlatformSettingsKeepAgentAndTerminalPullPoliciesIndependent(t *testing.T) {
	memory := &memoryStore{settings: store.PlatformSettings{DefaultEndpointProfileID: testProfileID, Revision: 1}}
	updated, err := NewService(memory, "", nil).UpdateSettings(context.Background(), SettingsInput{
		AgentImage: "registry.example.com/zke-agent:v1", AgentImagePullPolicy: "Never",
		ClusterTerminalImage: "registry.example.com/zke-terminal:v2", ClusterTerminalImagePullPolicy: "Always",
		ClusterTerminalSessionTTL: 15 * time.Minute,
		ExpectedRevision:          1, ActorUserID: testUserID, Now: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.AgentImagePullPolicy != "Never" || updated.ClusterTerminalImagePullPolicy != "Always" {
		t.Fatalf("pull policies = Agent %q, Terminal %q", updated.AgentImagePullPolicy, updated.ClusterTerminalImagePullPolicy)
	}
}

// The lifetime the Server previously clamped silently is now an explicit
// rejection, so a bad value cannot look accepted and then behave differently.
func TestPlatformSettingsRejectSessionTTLOutsideAllowedRange(t *testing.T) {
	for _, ttl := range []time.Duration{
		0,
		30 * time.Second,
		2 * time.Hour,
		90*time.Second + 500*time.Millisecond,
	} {
		memory := &memoryStore{settings: store.PlatformSettings{DefaultEndpointProfileID: testProfileID, Revision: 1}}
		_, err := NewService(memory, "", nil).UpdateSettings(context.Background(), SettingsInput{
			AgentImage: "registry.example.com/zke-agent:v1", AgentImagePullPolicy: "IfNotPresent",
			ClusterTerminalImage: "registry.example.com/zke-terminal:v1", ClusterTerminalImagePullPolicy: "IfNotPresent",
			ClusterTerminalSessionTTL: ttl,
			ExpectedRevision:          1, ActorUserID: testUserID, Now: time.Now().UTC(),
		})
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("UpdateSettings(%s) error = %v, want ErrInvalidInput", ttl, err)
		}
	}
}

func TestHTTPRegistrationEndpointDoesNotRequireLocalOptIn(t *testing.T) {
	now := time.Now().UTC()
	base := ProfileInput{Name: "Docker Desktop", RegistrationURL: "http://host.docker.internal:8080", QUICAddress: "host.docker.internal:8443", Enabled: true, ActorUserID: testUserID, Now: now}
	if err := validateProfileInput(base, false); err != nil {
		t.Fatalf("local HTTP endpoint: %v", err)
	}
	base.RegistrationURL = "http://zke.example.com:8080"
	if err := validateProfileInput(base, false); err != nil {
		t.Fatalf("remote HTTP endpoint: %v", err)
	}
}

func TestListenerSANsAreDerivedOnlyFromEnabledEndpointProfiles(t *testing.T) {
	dnsNames, ipAddresses, err := listenerSANs([]store.AgentEndpointProfile{
		{ID: "1", QUICAddress: "zke.example.com:8443", Enabled: true},
		{ID: "2", QUICAddress: "127.0.0.1:8443", Enabled: true},
		{ID: "3", QUICAddress: "disabled.example.com:8443", Enabled: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(dnsNames, []string{"zke.example.com"}) ||
		!reflect.DeepEqual(ipAddresses, []string{"127.0.0.1"}) {
		t.Fatalf("unexpected Listener SANs: DNS=%v IP=%v", dnsNames, ipAddresses)
	}
}

func TestPlatformDefaultEndpointIsListedFirst(t *testing.T) {
	profiles := []Profile{
		{ID: "first", Name: "First"},
		{ID: "default", Name: "Default"},
		{ID: "last", Name: "Last"},
	}
	ordered := defaultProfileFirst(profiles, "default")
	if ordered[0].ID != "default" || ordered[1].ID != "first" || ordered[2].ID != "last" {
		t.Fatalf("unexpected profile order: %+v", ordered)
	}
	if profiles[0].ID != "first" {
		t.Fatal("defaultProfileFirst mutated its input")
	}
}

func TestInvalidEndpointExplainsTheRejectedField(t *testing.T) {
	err := validateProfileInput(ProfileInput{
		Name: "Broken", RegistrationURL: "http://zke.example.com:8080",
		QUICAddress: "zke.example.com", ActorUserID: testUserID, Now: time.Now().UTC(),
	}, false)
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("error = %v, want ErrInvalidInput", err)
	}
	var detailed interface{ Detail() string }
	if !errors.As(err, &detailed) || detailed.Detail() != "QUIC 地址必须使用 host:port 格式" {
		t.Fatalf("error detail = %q", detailed.Detail())
	}
}

func TestResolveEnrollmentSnapshotCopiesProfileAndDefaults(t *testing.T) {
	certificateFile := listenerCertificate(t, []string{"zke.example.com"})
	memory := &memoryStore{profiles: map[string]store.AgentEndpointProfile{testProfileID: {ID: testProfileID, Name: "Public", RegistrationURL: "https://zke.example.com", QUICAddress: "zke.example.com:8443", Enabled: true, Revision: 3}}, settings: store.PlatformSettings{DefaultEndpointProfileID: testProfileID, AgentImage: "registry.example.com/zke-agent:v1", AgentImagePullPolicy: "IfNotPresent", Revision: 1}}
	snapshot, err := NewService(memory, certificateFile, nil).ResolveEnrollmentSnapshot(context.Background(), "", "custom-system")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.EndpointProfileRevision != 3 || snapshot.RegistrationURL != "https://zke.example.com" || snapshot.AgentImage != "registry.example.com/zke-agent:v1" || snapshot.AgentNamespace != "custom-system" {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
}

func TestEnabledEndpointCertificateIsReconciledWithoutRestart(t *testing.T) {
	certificateFile := listenerCertificate(t, []string{"zke.example.com"})
	memory := &memoryStore{profiles: map[string]store.AgentEndpointProfile{testProfileID: {ID: testProfileID, Name: "Public", RegistrationURL: "https://zke.example.com", QUICAddress: "zke.example.com:8443", Enabled: true, Revision: 1}}, settings: store.PlatformSettings{DefaultEndpointProfileID: "33333333-3333-4333-8333-333333333333"}}
	var reconciledDNSNames []string
	service := NewService(memory, certificateFile, func(_ context.Context, dnsNames, _ []string, _ time.Time) error {
		reconciledDNSNames = append([]string(nil), dnsNames...)
		writeListenerCertificate(t, certificateFile, dnsNames)
		return nil
	})
	updated, err := service.UpdateProfile(context.Background(), ProfileInput{ID: testProfileID, Name: "Changed", RegistrationURL: "https://new.example.com", QUICAddress: "new.example.com:8443", Enabled: true, ExpectedRevision: 1, ActorUserID: testUserID, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reconciledDNSNames, []string{"new.example.com"}) || updated.Status != ProfileStatusReady {
		t.Fatalf("reconciled DNS names = %v, status = %q", reconciledDNSNames, updated.Status)
	}
}

func TestDeletingEndpointRemovesItsListenerSANWithoutRestart(t *testing.T) {
	defaultID := "33333333-3333-4333-8333-333333333333"
	memory := &memoryStore{
		profiles: map[string]store.AgentEndpointProfile{
			testProfileID: {ID: testProfileID, Name: "Old", RegistrationURL: "https://old.example.com", QUICAddress: "old.example.com:8443", Enabled: true, Revision: 1},
			defaultID:     {ID: defaultID, Name: "Default", RegistrationURL: "https://zke.example.com", QUICAddress: "zke.example.com:8443", Enabled: true, Revision: 1},
		},
		settings: store.PlatformSettings{DefaultEndpointProfileID: defaultID},
	}
	var reconciledDNSNames []string
	service := NewService(memory, "", func(_ context.Context, dnsNames, _ []string, _ time.Time) error {
		reconciledDNSNames = append([]string(nil), dnsNames...)
		return nil
	})
	if err := service.DeleteProfile(context.Background(), testProfileID); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reconciledDNSNames, []string{"zke.example.com"}) {
		t.Fatalf("reconciled DNS names = %v", reconciledDNSNames)
	}
	if _, exists := memory.profiles[testProfileID]; exists {
		t.Fatal("deleted endpoint remains in store")
	}
}

func TestEndpointUpdateRestoresListenerSANsWhenDatabaseWriteFails(t *testing.T) {
	wantErr := errors.New("database write failed")
	memory := &memoryStore{
		profiles: map[string]store.AgentEndpointProfile{
			testProfileID: {ID: testProfileID, Name: "Old", RegistrationURL: "https://old.example.com", QUICAddress: "old.example.com:8443", Enabled: true, Revision: 1},
		},
		settings:  store.PlatformSettings{DefaultEndpointProfileID: "33333333-3333-4333-8333-333333333333"},
		updateErr: wantErr,
	}
	var reconciliations [][]string
	service := NewService(memory, "", func(_ context.Context, dnsNames, _ []string, _ time.Time) error {
		reconciliations = append(reconciliations, append([]string(nil), dnsNames...))
		return nil
	})
	_, err := service.UpdateProfile(context.Background(), ProfileInput{
		ID: testProfileID, Name: "New", RegistrationURL: "https://new.example.com",
		QUICAddress: "new.example.com:8443", Enabled: true, ExpectedRevision: 1,
		ActorUserID: testUserID, Now: time.Now().UTC(),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("UpdateProfile() error = %v, want database error", err)
	}
	wantReconciliations := [][]string{{"new.example.com"}, {"old.example.com"}}
	if !reflect.DeepEqual(reconciliations, wantReconciliations) {
		t.Fatalf("Listener reconciliations = %v, want %v", reconciliations, wantReconciliations)
	}
}

func TestPlatformDefaultEndpointCannotBeUpdated(t *testing.T) {
	memory := &memoryStore{
		profiles: map[string]store.AgentEndpointProfile{testProfileID: {
			ID: testProfileID, Name: "Default", RegistrationURL: "http://127.0.0.1:8080",
			QUICAddress: "127.0.0.1:8443", Enabled: true, Revision: 1,
		}},
		settings: store.PlatformSettings{DefaultEndpointProfileID: testProfileID},
	}
	_, err := NewService(memory, "", nil).UpdateProfile(context.Background(), ProfileInput{
		ID: testProfileID, Name: "Changed", RegistrationURL: "http://127.0.0.1:8080",
		QUICAddress: "127.0.0.1:8443", Enabled: true, ExpectedRevision: 1,
		ActorUserID: testUserID, Now: time.Now().UTC(),
	})
	if !errors.Is(err, ErrInUse) {
		t.Fatalf("UpdateProfile() error = %v, want ErrInUse", err)
	}
}

func TestProfileIsNotPersistedWhenCertificateReconciliationFails(t *testing.T) {
	certificateFile := listenerCertificate(t, []string{"zke.example.com"})
	memory := &memoryStore{
		profiles: map[string]store.AgentEndpointProfile{},
		settings: store.PlatformSettings{DefaultEndpointProfileID: testProfileID},
	}
	wantErr := errors.New("certificate reconciliation failed")
	service := NewService(memory, certificateFile, func(context.Context, []string, []string, time.Time) error {
		return wantErr
	})
	_, err := service.CreateProfile(context.Background(), ProfileInput{
		Name: "New", RegistrationURL: "https://new.example.com", QUICAddress: "new.example.com:8443",
		Enabled: true, ActorUserID: testUserID, Now: time.Now().UTC(),
	})
	if !errors.Is(err, wantErr) {
		t.Fatalf("create error = %v, want reconciliation error", err)
	}
	if len(memory.profiles) != 0 {
		t.Fatalf("profile was persisted after certificate failure: %+v", memory.profiles)
	}
}

func listenerCertificate(t *testing.T, dnsNames []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "listener.crt")
	writeListenerCertificate(t, path, dnsNames)
	return path
}

func writeListenerCertificate(t *testing.T, path string, dnsNames []string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test listener"}, DNSNames: dnsNames, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
}
