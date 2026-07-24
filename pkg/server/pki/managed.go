package pki

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	AgentClientCACertificateName   = "agent-client-ca.crt"
	AgentClientCAPrivateKeyName    = "agent-client-ca.key"
	AgentListenerCACertificateName = "agent-listener-ca.crt"
	AgentListenerCAPrivateKeyName  = "agent-listener-ca.key"
	AgentListenerCertificateName   = "agent-listener.crt"
	AgentListenerPrivateKeyName    = "agent-listener.key"
)

var allFileNames = []string{
	AgentClientCACertificateName,
	AgentClientCAPrivateKeyName,
	AgentListenerCACertificateName,
	AgentListenerCAPrivateKeyName,
	AgentListenerCertificateName,
	AgentListenerPrivateKeyName,
}

type Config struct {
	Directory                string
	AutoGenerate             bool
	AgentClientCAValidity    time.Duration
	AgentListenerCAValidity  time.Duration
	AgentListenerValidity    time.Duration
	AgentListenerRenewBefore time.Duration
	ListenerDNSNames         []string
	ListenerIPAddresses      []string
}

type Files struct {
	AgentClientCACertificate   string
	AgentClientCAPrivateKey    string
	AgentListenerCACertificate string
	AgentListenerCAPrivateKey  string
	AgentListenerCertificate   string
	AgentListenerPrivateKey    string
	State                      store.ServerPKIState
}

type material struct {
	clientCACertificate   *x509.Certificate
	clientCAPrivateKey    crypto.Signer
	listenerCACertificate *x509.Certificate
	listenerCAPrivateKey  crypto.Signer
	listenerCertificate   *x509.Certificate
	listenerPrivateKey    crypto.Signer
}

func Ensure(
	ctx context.Context,
	pool *pgxpool.Pool,
	config Config,
	now time.Time,
) (Files, error) {
	lock, err := store.AcquireServerPKILock(ctx, pool)
	if err != nil {
		return Files{}, err
	}
	defer func() {
		unlockContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = lock.Close(unlockContext)
	}()

	storedState, stateExists, err := lock.Load(ctx)
	if err != nil {
		return Files{}, err
	}
	if err := os.MkdirAll(config.Directory, 0o700); err != nil {
		return Files{}, fmt.Errorf("create managed Server PKI directory: %w", err)
	}
	if err := os.Chmod(config.Directory, 0o700); err != nil {
		return Files{}, fmt.Errorf("secure managed Server PKI directory: %w", err)
	}
	paths := managedPaths(config.Directory)
	existing, err := existingFileCount(paths)
	if err != nil {
		return Files{}, err
	}
	if existing != 0 && existing != len(allFileNames) {
		return Files{}, errors.New(
			"managed Server PKI directory contains a partial certificate set; restore the PV or remove the incomplete new deployment directory",
		)
	}
	if existing == 0 {
		if stateExists {
			return Files{}, errors.New(
				"managed Server PKI database state exists but certificate files are missing; restore the original PV instead of generating a new CA",
			)
		}
		hasAgentState, err := lock.HasAgentSecurityState(ctx)
		if err != nil {
			return Files{}, err
		}
		if hasAgentState {
			return Files{}, errors.New(
				"managed Server PKI files and fingerprints are absent but Agent security state already exists; provide the original PKI files instead of generating new CAs",
			)
		}
		if !config.AutoGenerate {
			return Files{}, errors.New("managed Server PKI files are absent and automatic generation is disabled")
		}
		if err := generateAll(paths, config, now); err != nil {
			return Files{}, err
		}
	}

	loaded, err := loadAndValidate(paths, config, now)
	if err != nil {
		return Files{}, err
	}
	currentState := stateFromMaterial(loaded)
	if stateExists && !sameRoots(storedState, currentState) {
		return Files{}, errors.New(
			"managed Server PKI CA fingerprints do not match the database; restore the original PV and do not rotate CAs implicitly",
		)
	}
	if stateExists &&
		storedState.AgentListenerCertificateFingerprint !=
			currentState.AgentListenerCertificateFingerprint {
		return Files{}, errors.New(
			"managed Agent Listener certificate fingerprint does not match the database",
		)
	}

	if !loaded.listenerCertificate.NotAfter.After(now.Add(config.AgentListenerRenewBefore)) {
		if err := renewListener(paths, config, loaded, now); err != nil {
			return Files{}, err
		}
		loaded, err = loadAndValidate(paths, config, now)
		if err != nil {
			return Files{}, err
		}
		currentState = stateFromMaterial(loaded)
	}
	if err := lock.Save(ctx, currentState); err != nil {
		return Files{}, err
	}
	paths.State = currentState
	return paths, nil
}

func managedPaths(directory string) Files {
	return Files{
		AgentClientCACertificate:   filepath.Join(directory, AgentClientCACertificateName),
		AgentClientCAPrivateKey:    filepath.Join(directory, AgentClientCAPrivateKeyName),
		AgentListenerCACertificate: filepath.Join(directory, AgentListenerCACertificateName),
		AgentListenerCAPrivateKey:  filepath.Join(directory, AgentListenerCAPrivateKeyName),
		AgentListenerCertificate:   filepath.Join(directory, AgentListenerCertificateName),
		AgentListenerPrivateKey:    filepath.Join(directory, AgentListenerPrivateKeyName),
	}
}

func existingFileCount(paths Files) (int, error) {
	count := 0
	for _, path := range []string{
		paths.AgentClientCACertificate,
		paths.AgentClientCAPrivateKey,
		paths.AgentListenerCACertificate,
		paths.AgentListenerCAPrivateKey,
		paths.AgentListenerCertificate,
		paths.AgentListenerPrivateKey,
	} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, fmt.Errorf("inspect managed Server PKI file %q: %w", path, err)
		}
		if !info.Mode().IsRegular() {
			return 0, fmt.Errorf("managed Server PKI path %q is not a regular file", path)
		}
		count++
	}
	return count, nil
}

func generateAll(paths Files, config Config, now time.Time) error {
	clientCACert, clientCAKey, clientCertPEM, clientKeyPEM, err := newCA(
		"ZKE Agent Client CA",
		config.AgentClientCAValidity,
		now,
	)
	if err != nil {
		return err
	}
	_ = clientCACert
	_ = clientCAKey
	listenerCACert, listenerCAKey, listenerCACertPEM, listenerCAKeyPEM, err := newCA(
		"ZKE Agent Listener CA",
		config.AgentListenerCAValidity,
		now,
	)
	if err != nil {
		return err
	}
	listenerCertPEM, listenerKeyPEM, _, _, err := newListenerCertificate(
		listenerCACert,
		listenerCAKey,
		nil,
		config,
		now,
	)
	if err != nil {
		return err
	}
	for _, item := range []struct {
		path string
		data []byte
		mode os.FileMode
	}{
		{paths.AgentClientCACertificate, clientCertPEM, 0o644},
		{paths.AgentClientCAPrivateKey, clientKeyPEM, 0o600},
		{paths.AgentListenerCACertificate, listenerCACertPEM, 0o644},
		{paths.AgentListenerCAPrivateKey, listenerCAKeyPEM, 0o600},
		{paths.AgentListenerCertificate, listenerCertPEM, 0o644},
		{paths.AgentListenerPrivateKey, listenerKeyPEM, 0o600},
	} {
		if err := atomicWrite(item.path, item.data, item.mode); err != nil {
			return err
		}
	}
	return nil
}

func renewListener(
	paths Files,
	config Config,
	loaded material,
	now time.Time,
) error {
	certificatePEM, _, _, _, err := newListenerCertificate(
		loaded.listenerCACertificate,
		loaded.listenerCAPrivateKey,
		loaded.listenerPrivateKey,
		config,
		now,
	)
	if err != nil {
		return err
	}
	if err := atomicWrite(paths.AgentListenerCertificate, certificatePEM, 0o644); err != nil {
		return err
	}
	return nil
}

func newCA(
	commonName string,
	validity time.Duration,
	now time.Time,
) (*x509.Certificate, *ecdsa.PrivateKey, []byte, []byte, error) {
	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("generate %s private key: %w", commonName, err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName, Organization: []string{"ZKE"}},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(validity),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &privateKey.PublicKey, privateKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("create %s certificate: %w", commonName, err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("parse generated %s certificate: %w", commonName, err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal %s private key: %w", commonName, err)
	}
	return certificate, privateKey,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		nil
}

func newListenerCertificate(
	caCertificate *x509.Certificate,
	caPrivateKey crypto.Signer,
	privateKey crypto.Signer,
	config Config,
	now time.Time,
) ([]byte, []byte, *x509.Certificate, crypto.Signer, error) {
	if privateKey == nil {
		var err error
		privateKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, nil, nil, fmt.Errorf("generate Agent Listener private key: %w", err)
		}
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	notAfter := now.Add(config.AgentListenerValidity)
	if notAfter.After(caCertificate.NotAfter) {
		notAfter = caCertificate.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "ZKE Agent Listener",
			Organization: []string{"ZKE"},
		},
		NotBefore:   now.Add(-5 * time.Minute),
		NotAfter:    notAfter,
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:    append([]string(nil), config.ListenerDNSNames...),
	}
	for _, address := range config.ListenerIPAddresses {
		template.IPAddresses = append(template.IPAddresses, net.ParseIP(address))
	}
	der, err := x509.CreateCertificate(
		rand.Reader,
		template,
		caCertificate,
		privateKey.Public(),
		caPrivateKey,
	)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("create Agent Listener certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("parse generated Agent Listener certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("marshal Agent Listener private key: %w", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		certificate,
		privateKey,
		nil
}

func loadAndValidate(paths Files, config Config, now time.Time) (material, error) {
	clientCA, clientKey, err := loadCA(
		paths.AgentClientCACertificate,
		paths.AgentClientCAPrivateKey,
		"Agent Client CA",
		now,
	)
	if err != nil {
		return material{}, err
	}
	listenerCA, listenerCAKey, err := loadCA(
		paths.AgentListenerCACertificate,
		paths.AgentListenerCAPrivateKey,
		"Agent Listener CA",
		now,
	)
	if err != nil {
		return material{}, err
	}
	listenerCertificate, listenerKey, err := loadCertificateAndKey(
		paths.AgentListenerCertificate,
		paths.AgentListenerPrivateKey,
	)
	if err != nil {
		return material{}, fmt.Errorf("load Agent Listener identity: %w", err)
	}
	if err := listenerCertificate.CheckSignatureFrom(listenerCA); err != nil {
		return material{}, fmt.Errorf("verify Agent Listener certificate signature: %w", err)
	}
	if !slices.Contains(
		listenerCertificate.ExtKeyUsage,
		x509.ExtKeyUsageServerAuth,
	) {
		return material{}, errors.New("Agent Listener certificate is not valid for Server authentication")
	}
	for _, dnsName := range config.ListenerDNSNames {
		if !slices.Contains(listenerCertificate.DNSNames, dnsName) {
			return material{}, fmt.Errorf("Agent Listener certificate is missing configured DNS SAN %q", dnsName)
		}
	}
	for _, address := range config.ListenerIPAddresses {
		found := false
		for _, certificateIP := range listenerCertificate.IPAddresses {
			if certificateIP.Equal(net.ParseIP(address)) {
				found = true
				break
			}
		}
		if !found {
			return material{}, fmt.Errorf("Agent Listener certificate is missing configured IP SAN %q", address)
		}
	}
	return material{
		clientCACertificate:   clientCA,
		clientCAPrivateKey:    clientKey,
		listenerCACertificate: listenerCA,
		listenerCAPrivateKey:  listenerCAKey,
		listenerCertificate:   listenerCertificate,
		listenerPrivateKey:    listenerKey,
	}, nil
}

func loadCA(
	certificatePath string,
	privateKeyPath string,
	name string,
	now time.Time,
) (*x509.Certificate, crypto.Signer, error) {
	certificate, privateKey, err := loadCertificateAndKey(certificatePath, privateKeyPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load %s: %w", name, err)
	}
	if !certificate.IsCA ||
		certificate.KeyUsage&x509.KeyUsageCertSign == 0 ||
		!certificate.NotAfter.After(now) {
		return nil, nil, fmt.Errorf("%s certificate is not a usable, unexpired CA", name)
	}
	return certificate, privateKey, nil
}

func loadCertificateAndKey(
	certificatePath string,
	privateKeyPath string,
) (*x509.Certificate, crypto.Signer, error) {
	certificatePEM, err := os.ReadFile(certificatePath)
	if err != nil {
		return nil, nil, err
	}
	certificateBlock, _ := pem.Decode(certificatePEM)
	if certificateBlock == nil || certificateBlock.Type != "CERTIFICATE" {
		return nil, nil, errors.New("certificate PEM is invalid")
	}
	certificate, err := x509.ParseCertificate(certificateBlock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	privateKeyPEM, err := os.ReadFile(privateKeyPath)
	if err != nil {
		return nil, nil, err
	}
	privateKeyBlock, _ := pem.Decode(privateKeyPEM)
	if privateKeyBlock == nil {
		return nil, nil, errors.New("private key PEM is invalid")
	}
	var parsedKey any
	switch privateKeyBlock.Type {
	case "PRIVATE KEY":
		parsedKey, err = x509.ParsePKCS8PrivateKey(privateKeyBlock.Bytes)
	case "EC PRIVATE KEY":
		parsedKey, err = x509.ParseECPrivateKey(privateKeyBlock.Bytes)
	case "RSA PRIVATE KEY":
		parsedKey, err = x509.ParsePKCS1PrivateKey(privateKeyBlock.Bytes)
	default:
		return nil, nil, errors.New("private key PEM type is unsupported")
	}
	if err != nil {
		return nil, nil, errors.New("private key is invalid")
	}
	privateKey, ok := parsedKey.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("private key cannot sign certificates")
	}
	certificatePublicKey, err := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil {
		return nil, nil, errors.New("certificate public key is invalid")
	}
	privatePublicKey, err := x509.MarshalPKIXPublicKey(privateKey.Public())
	if err != nil || !bytes.Equal(certificatePublicKey, privatePublicKey) {
		return nil, nil, errors.New("certificate and private key do not match")
	}
	return certificate, privateKey, nil
}

func stateFromMaterial(loaded material) store.ServerPKIState {
	return store.ServerPKIState{
		AgentClientCAFingerprint:            fingerprint(loaded.clientCACertificate),
		AgentClientCAExpiresAt:              loaded.clientCACertificate.NotAfter,
		AgentListenerCAFingerprint:          fingerprint(loaded.listenerCACertificate),
		AgentListenerCAExpiresAt:            loaded.listenerCACertificate.NotAfter,
		AgentListenerCertificateFingerprint: fingerprint(loaded.listenerCertificate),
		AgentListenerCertificateExpiresAt:   loaded.listenerCertificate.NotAfter,
	}
}

func sameRoots(left, right store.ServerPKIState) bool {
	return left.AgentClientCAFingerprint == right.AgentClientCAFingerprint &&
		left.AgentListenerCAFingerprint == right.AgentListenerCAFingerprint
}

func fingerprint(certificate *x509.Certificate) string {
	sum := sha256.Sum256(certificate.Raw)
	return hex.EncodeToString(sum[:])
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, errors.New("generate certificate serial")
	}
	if serial.Sign() == 0 {
		return big.NewInt(1), nil
	}
	return serial, nil
}

func atomicWrite(path string, data []byte, mode os.FileMode) error {
	file, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary managed PKI file: %w", err)
	}
	tempPath := file.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempPath)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return fmt.Errorf("set managed PKI file permissions: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write managed PKI file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync managed PKI file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close managed PKI file: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("publish managed PKI file: %w", err)
	}
	removeTemp = false
	return nil
}

func CertificatePEM(path string) ([]byte, error) {
	value, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, rest := pem.Decode(value)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("certificate file must contain exactly one PEM certificate")
	}
	return value, nil
}
