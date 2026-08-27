package helm

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/togettoyou/zke/pkg/server/store"
)

// A signing key and the armored public half of it, as an administrator would
// paste it into the repository form.
func signingKey(t *testing.T, identity string) (*openpgp.Entity, string) {
	t.Helper()
	entity, err := openpgp.NewEntity(identity, "chart signing", identity+"@example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	buffer := &bytes.Buffer{}
	writer, err := armor.Encode(buffer, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := entity.Serialize(writer); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return entity, buffer.String()
}

// signProvenance writes what `helm package --sign` writes: the chart's metadata,
// a YAML document terminator, and a table of file digests, all clear-signed.
//
// The digest is a parameter rather than computed from the archive so that a test
// can produce the one case that matters most — a correctly signed table that
// does not describe the archive it arrived with.
func signProvenance(
	t *testing.T,
	entity *openpgp.Entity,
	fileName string,
	digest string,
) []byte {
	t.Helper()
	body := fmt.Sprintf(
		"name: demo\nversion: 1.2.0\n...\nfiles:\n  %s: %s\n",
		fileName,
		digest,
	)
	buffer := &bytes.Buffer{}
	writer, err := clearsign.Encode(buffer, entity.PrivateKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func archiveDigest(archive []byte) string {
	sum := sha256.Sum256(archive)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func signedRepository(url string, policy SignaturePolicy, keyring string) store.HelmRepository {
	repository := testRepository(url)
	repository.SignaturePolicy = string(policy)
	repository.PublicKeyring = keyring
	return repository
}

// A chart signed by a key the repository declares is admitted, and the identity
// that signed it travels with the answer — "verified" on its own does not say
// which of several trusted publishers produced this.
func TestSignedChartIsVerifiedAndAttributed(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "demo", "1.2.0")
	entity, keyring := signingKey(t, "release-bot")
	server := newRepositoryServer(t, archive)
	server.provenance = map[string][]byte{
		"/charts/demo-1.2.0.tgz.prov": signProvenance(
			t,
			entity,
			"demo-1.2.0.tgz",
			archiveDigest(archive),
		),
	}
	service, _ := newTestService(t, signedRepository(server.URL, SignatureRequired, keyring))

	detail, err := service.GetChart(context.Background(), testRepositoryID, "demo", "1.2.0")
	if err != nil {
		t.Fatalf("GetChart() = %v", err)
	}
	if !detail.Signature.Verified || detail.Signature.Policy != SignatureRequired {
		t.Fatalf("signature = %+v, want a verified required policy", detail.Signature)
	}
	if detail.Signature.Digest != archiveDigest(archive) {
		t.Fatalf("signature digest = %q, want the archive's", detail.Signature.Digest)
	}
	if len(detail.Signature.SignedBy) == 0 ||
		!strings.Contains(detail.Signature.SignedBy[0], "release-bot") {
		t.Fatalf("signed by = %v, want the signing identity", detail.Signature.SignedBy)
	}
}

// The two policies that admit an unsigned chart and the one that does not. This
// is the whole point of having three states rather than a boolean: a repository
// mid-rollout publishes signatures for some versions and not others.
func TestUnsignedChartFollowsThePolicy(t *testing.T) {
	t.Parallel()

	_, keyring := signingKey(t, "release-bot")
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))

	required, _ := newTestService(t, signedRepository(server.URL, SignatureRequired, keyring))
	_, err := required.GetChart(context.Background(), testRepositoryID, "demo", "1.2.0")
	if !errors.Is(err, ErrChartUnsigned) {
		t.Fatalf("GetChart() under `required` = %v, want ErrChartUnsigned", err)
	}

	permissive, _ := newTestService(t, signedRepository(server.URL, SignatureIfPresent, keyring))
	detail, err := permissive.GetChart(context.Background(), testRepositoryID, "demo", "1.2.0")
	if err != nil {
		t.Fatalf("GetChart() under `verify_if_present` = %v", err)
	}
	// Unsigned rather than simply unverified: the Console has to be able to say
	// why there is no signature, and "the repository publishes none" and "it
	// did not check out" are opposite answers.
	if detail.Signature.Verified || !detail.Signature.Unsigned {
		t.Fatalf("signature = %+v, want unsigned and unverified", detail.Signature)
	}
}

// A valid signature over a digest that is not this archive's is the case the
// whole mechanism exists for: the repository served bytes that its publisher
// did not sign.
func TestArchiveThatDoesNotMatchItsSignatureIsRefused(t *testing.T) {
	t.Parallel()

	entity, keyring := signingKey(t, "release-bot")
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	server.provenance = map[string][]byte{
		"/charts/demo-1.2.0.tgz.prov": signProvenance(
			t,
			entity,
			"demo-1.2.0.tgz",
			"sha256:"+strings.Repeat("00", 32),
		),
	}
	service, _ := newTestService(t, signedRepository(server.URL, SignatureRequired, keyring))

	_, err := service.GetChart(context.Background(), testRepositoryID, "demo", "1.2.0")
	if !errors.Is(err, ErrChartSignatureInvalid) {
		t.Fatalf("GetChart() = %v, want ErrChartSignatureInvalid", err)
	}

	// And `verify_if_present` refuses it too. That policy admits a chart with
	// no signature; a signature that fails is not that case.
	permissive, _ := newTestService(t, signedRepository(server.URL, SignatureIfPresent, keyring))
	if _, err := permissive.GetChart(
		context.Background(),
		testRepositoryID,
		"demo",
		"1.2.0",
	); !errors.Is(err, ErrChartSignatureInvalid) {
		t.Fatalf("GetChart() under `verify_if_present` = %v, want ErrChartSignatureInvalid", err)
	}
}

// A perfectly valid signature from a key this repository does not declare is
// not a signature at all. Without this the mechanism would verify that somebody
// signed the chart, which is not the question.
func TestSignatureFromAnUntrustedKeyIsRefused(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "demo", "1.2.0")
	attacker, _ := signingKey(t, "someone-else")
	_, trusted := signingKey(t, "release-bot")
	server := newRepositoryServer(t, archive)
	server.provenance = map[string][]byte{
		"/charts/demo-1.2.0.tgz.prov": signProvenance(
			t,
			attacker,
			"demo-1.2.0.tgz",
			archiveDigest(archive),
		),
	}
	service, _ := newTestService(t, signedRepository(server.URL, SignatureRequired, trusted))

	_, err := service.GetChart(context.Background(), testRepositoryID, "demo", "1.2.0")
	if !errors.Is(err, ErrChartSignatureInvalid) {
		t.Fatalf("GetChart() = %v, want ErrChartSignatureInvalid", err)
	}
}

// The refusal happens before the Agent is asked to do anything, so a chart that
// does not verify never reaches a Cluster and the caller's idempotency key is
// still theirs to retry with.
func TestInstallRefusesAChartThatDoesNotVerify(t *testing.T) {
	t.Parallel()

	_, keyring := signingKey(t, "release-bot")
	server := newRepositoryServer(t, chartArchive(t, "demo", "1.2.0"))
	service, agent := newTestService(t, signedRepository(server.URL, SignatureRequired, keyring))

	_, err := service.Install(context.Background(), InstallInput{
		ClusterID:      testClusterID,
		Namespace:      "shop",
		Name:           "checkout",
		RepositoryID:   testRepositoryID,
		Chart:          "demo",
		Version:        "1.2.0",
		IdempotencyKey: "key-1",
	})
	if !errors.Is(err, ErrChartUnsigned) {
		t.Fatalf("Install() = %v, want ErrChartUnsigned", err)
	}
	if agent.request != nil {
		t.Fatal("Install() reached the Agent with a chart that was not verified")
	}
}

// A cache hit is verified too. The archive on disk was fetched under whatever
// policy was in force then; the answer has to come from the keyring in force
// now, or revoking a key would leave every already-cached chart installable.
func TestCachedArchiveIsVerifiedAgainstTheCurrentKeyring(t *testing.T) {
	t.Parallel()

	archive := chartArchive(t, "demo", "1.2.0")
	entity, keyring := signingKey(t, "release-bot")
	server := newRepositoryServer(t, archive)
	server.provenance = map[string][]byte{
		"/charts/demo-1.2.0.tgz.prov": signProvenance(
			t,
			entity,
			"demo-1.2.0.tgz",
			archiveDigest(archive),
		),
	}
	repositories := &stubRepositoryStore{
		repository: signedRepository(server.URL, SignatureRequired, keyring),
	}
	service, _ := newTestServiceWithStore(t, repositories, t.TempDir())

	if _, err := service.GetChart(
		context.Background(),
		testRepositoryID,
		"demo",
		"1.2.0",
	); err != nil {
		t.Fatalf("first GetChart() = %v", err)
	}

	// The key is replaced without touching the cache — which is what a
	// rotation looks like from the catalogue's side.
	_, replacement := signingKey(t, "new-bot")
	repositories.repository.PublicKeyring = replacement
	if _, err := service.GetChart(
		context.Background(),
		testRepositoryID,
		"demo",
		"1.2.0",
	); !errors.Is(err, ErrChartSignatureInvalid) {
		t.Fatalf("second GetChart() = %v, want ErrChartSignatureInvalid", err)
	}
}

// A policy with no keys behind it would refuse everything under `required` and
// — far worse — admit everything under `verify_if_present` while the page said
// signatures were being checked. It is refused where it is entered.
func TestSignaturePolicyRequiresKeys(t *testing.T) {
	t.Parallel()

	_, keyring := signingKey(t, "release-bot")
	cases := []struct {
		name     string
		input    RepositoryInput
		accepted bool
	}{
		{
			name:  "policy without keys",
			input: RepositoryInput{SignaturePolicy: string(SignatureRequired)},
		},
		{
			name: "keys that are not PGP armor",
			input: RepositoryInput{
				SignaturePolicy: string(SignatureRequired),
				PublicKeyring:   "-----BEGIN CERTIFICATE-----\nnope\n-----END CERTIFICATE-----",
			},
		},
		{
			name:  "an unknown policy",
			input: RepositoryInput{SignaturePolicy: "trust-me", PublicKeyring: keyring},
		},
		{
			name: "policy and keys together",
			input: RepositoryInput{
				SignaturePolicy: string(SignatureRequired),
				PublicKeyring:   keyring,
			},
			accepted: true,
		},
		{
			// Keys with no policy are harmless, and refusing them would stop an
			// administrator loading the keyring before switching over.
			name:     "keys without a policy",
			input:    RepositoryInput{PublicKeyring: keyring},
			accepted: true,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			input := testCase.input
			input.Name = "demo"
			input.URL = "https://charts.example.test"
			_, err := normalizeRepositoryInput(input)
			if testCase.accepted && err != nil {
				t.Fatalf("normalizeRepositoryInput() = %v, want accepted", err)
			}
			if !testCase.accepted && !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("normalizeRepositoryInput() = %v, want ErrInvalidInput", err)
			}
		})
	}
}

// What the catalogue reports about a repository's keys: the fingerprint, which
// is the only part of a PGP key that identifies it, and the identities, which
// are what a person reads.
func TestKeyringIsReportedAsIdentitiesAndFingerprints(t *testing.T) {
	t.Parallel()

	entity, keyring := signingKey(t, "release-bot")
	keys := describeKeyring(keyring)
	if len(keys) != 1 {
		t.Fatalf("describeKeyring() returned %d keys, want 1", len(keys))
	}
	want := strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint))
	if keys[0].Fingerprint != want {
		t.Fatalf("fingerprint = %q, want %q", keys[0].Fingerprint, want)
	}
	if len(keys[0].Identities) != 1 || !strings.Contains(keys[0].Identities[0], "release-bot") {
		t.Fatalf("identities = %v", keys[0].Identities)
	}
	// A keyring that no longer parses reports nothing rather than failing a
	// listing: it was checked when it was stored, and the policy still refuses
	// every chart it was supposed to verify.
	if describeKeyring("not a key") != nil {
		t.Fatal("describeKeyring() invented keys for unparseable armor")
	}
}

// A stored policy this Server does not recognise is read as the strict one. The
// row was written by a version that knew a policy this one does not, and the
// safe reading of an instruction you cannot parse is not "check nothing".
func TestUnknownStoredPolicyIsReadStrictly(t *testing.T) {
	t.Parallel()

	if policy := storedSignaturePolicy("from-the-future"); policy != SignatureRequired {
		t.Fatalf("storedSignaturePolicy() = %q, want %q", policy, SignatureRequired)
	}
	if policy := storedSignaturePolicy(""); policy != SignatureDisabled {
		t.Fatalf("storedSignaturePolicy(\"\") = %q, want %q", policy, SignatureDisabled)
	}
}
