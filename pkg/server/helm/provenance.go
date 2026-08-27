package helm

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"helm.sh/helm/v3/pkg/provenance"
	"sigs.k8s.io/yaml"
)

// Chart provenance: deciding whether an archive is the one its publisher signed.
//
// Everything else in this package establishes that an archive is what the
// *repository* is serving — the index digest, the OCI manifest digest, the
// cache's own sidecar. None of that says anything about who produced it. A
// repository is a web server; whoever can write to it can publish a chart, and
// a mirror or a proxy in front of it can rewrite one in flight.
//
// Helm's answer is a detached signature published beside the archive:
// `<chart>-<version>.tgz.prov` is a PGP clear-signed document whose body is the
// chart's Chart.yaml followed by a table of file digests. Verifying it proves
// two things at once — that a key the platform trusts signed the table, and
// that the archive in hand hashes to what the table says.
//
// So the trust decision belongs to the repository entry: a repository is one
// publisher, its keys are a property of it, and an administrator sets the
// policy per repository rather than for the platform. Public charts nobody
// signs stay on SignatureDisabled; an internal repository moves to
// SignatureRequired the day its pipeline starts signing, passing through
// SignatureIfPresent while that rolls out.
//
// The verification is done here rather than by calling Helm's own Signatory,
// which reads a keyring and an archive from *files*. Both are already in memory
// on this Server, and writing them out to a temporary directory in order to
// read them back would add a filesystem to the trust path for no gain. What is
// reused is the part that is a format rather than a mechanism:
// provenance.SumCollection is the shape of the signed digest table.

// SignaturePolicy is what a repository requires of a chart's provenance.
type SignaturePolicy string

const (
	// SignatureDisabled does not fetch provenance and does not check it. It is
	// the default, and it is what a public repository that publishes no
	// signatures has to stay on.
	SignatureDisabled SignaturePolicy = "disabled"
	// SignatureIfPresent verifies a chart that publishes a `.prov` and admits
	// one that does not. It is a migration state, not a security boundary: a
	// mirror that can replace an archive can also remove the file next to it.
	SignatureIfPresent SignaturePolicy = "verify_if_present"
	// SignatureRequired admits nothing this Server cannot attribute to one of
	// the repository's keys.
	SignatureRequired SignaturePolicy = "required"
)

var (
	// ErrChartUnsigned is a repository that requires signatures serving a chart
	// version without one. It is separate from a bad signature because the two
	// have different people to talk to: an unsigned chart is a publishing gap,
	// an invalid one is a chart that is not what it claims to be.
	ErrChartUnsigned = errors.New("chart version publishes no provenance and this repository requires one")
	// ErrChartSignatureInvalid is a provenance file that does not verify: no
	// trusted key signed it, or the archive does not hash to what it says.
	ErrChartSignatureInvalid = errors.New("chart provenance did not verify against this repository's keys")
)

const (
	// Bound on a provenance document. It is a Chart.yaml and a short digest
	// table wrapped in an armored signature — kilobytes, and a repository
	// answering this URL with something enormous is not answering with one.
	maxProvenanceBytes int64 = 1 << 20
	// Bound on the stored keyring, matching the column. A keyring is a handful
	// of public keys; this leaves room for a platform that rotates often.
	maxKeyringBytes = 256 << 10
	// Bound on how many keys one repository may declare. It exists so that a
	// verification failure costs a bounded number of signature checks.
	maxKeyringEntries = 64
)

// SigningKey is one key a repository's charts may be signed with, as the API
// reports it.
//
// The stored value is an armor block; this is what an administrator actually
// asked when they opened the page — which keys does this repository trust, and
// are they the ones we think they are. The fingerprint is included because it
// is the only part of a PGP key that identifies it: a user ID is free text the
// key's own owner wrote.
type SigningKey struct {
	Fingerprint string   `json:"fingerprint"`
	KeyID       string   `json:"key_id"`
	Identities  []string `json:"identities"`
}

// ChartSignature is what verification concluded about one chart archive.
//
// It is reported for every fetch, including under SignatureDisabled, so the
// catalogue can say "this repository does not check signatures" rather than
// leaving the reader to infer it from a missing field.
type ChartSignature struct {
	Policy SignaturePolicy `json:"policy"`
	// Verified says a key on this repository's keyring signed a digest table
	// that names this archive's SHA-256. It is the only field that means
	// anything on its own.
	Verified bool `json:"verified"`
	// Unsigned says the repository publishes no provenance for this version.
	// Under SignatureIfPresent that is admitted, and this is how the Console
	// says so instead of showing an unexplained unverified badge.
	Unsigned bool `json:"unsigned"`
	// SignedBy and KeyID identify the key that signed it, for a reader who
	// wants to know which of several trusted publishers this came from.
	SignedBy []string `json:"signed_by,omitempty"`
	KeyID    string   `json:"key_id,omitempty"`
	// Digest is the archive's SHA-256, prefixed with its algorithm the way the
	// provenance file writes it. Under a verified signature it is what the
	// publisher signed; otherwise it is simply what this Server hashed, and the
	// distinction is Verified.
	Digest string `json:"digest,omitempty"`
	// FileName is the entry in the signed digest table that matched. A
	// provenance file signs archives by name, so which name matched is part of
	// what was proven.
	FileName string `json:"file_name,omitempty"`
}

// normalizeSignaturePolicy accepts what an administrator submitted, treating an
// empty value as "not configured" rather than as an error — a client that
// predates the field must not silently turn verification on or off.
func normalizeSignaturePolicy(value string) (SignaturePolicy, error) {
	switch SignaturePolicy(strings.TrimSpace(value)) {
	case "", SignatureDisabled:
		return SignatureDisabled, nil
	case SignatureIfPresent:
		return SignatureIfPresent, nil
	case SignatureRequired:
		return SignatureRequired, nil
	default:
		return "", invalid(
			"signature policy must be one of %q, %q or %q",
			SignatureDisabled,
			SignatureIfPresent,
			SignatureRequired,
		)
	}
}

// storedSignaturePolicy reads the column back. An unknown value is treated as
// SignatureRequired rather than as disabled: the row was written by a version
// of this Server that knew a policy this one does not, and the safe reading of
// an instruction you cannot parse is the strict one.
func storedSignaturePolicy(value string) SignaturePolicy {
	switch policy := SignaturePolicy(value); policy {
	case SignatureDisabled, SignatureIfPresent, SignatureRequired:
		return policy
	case "":
		return SignatureDisabled
	default:
		return SignatureRequired
	}
}

// parseKeyring turns the stored armor block into keys.
//
// It is called both when an administrator saves a repository — so an unusable
// keyring is refused at the point somebody can fix it — and before each
// verification, because the keyring is stored as text and this Server holds no
// parsed copy of it. Parsing a few public keys is cheap next to one HTTP
// request to the repository it belongs to.
func parseKeyring(armored string) (openpgp.EntityList, error) {
	trimmed := strings.TrimSpace(armored)
	if trimmed == "" {
		return nil, nil
	}
	if len(trimmed) > maxKeyringBytes {
		return nil, invalid("public keyring exceeds %d bytes", maxKeyringBytes)
	}
	entities, err := openpgp.ReadArmoredKeyRing(strings.NewReader(trimmed))
	if err != nil {
		// The parser's own message names what it choked on, which is what an
		// administrator pasting a key needs to see. Every failure here is a
		// rejection of submitted input, so they all read as one.
		return nil, invalid("public keyring is not ASCII-armored PGP public keys: %s", err)
	}
	if len(entities) == 0 {
		return nil, invalid("public keyring holds no PGP public keys")
	}
	if len(entities) > maxKeyringEntries {
		return nil, invalid("public keyring holds more than %d keys", maxKeyringEntries)
	}
	return entities, nil
}

// describeKeyring reports the keys without the armor.
//
// A keyring that no longer parses returns nothing rather than an error: it was
// checked when it was stored, so this is a row written by something else, and a
// repository listing is not the place to fail over it. The policy still applies
// and a verification against it still fails loudly.
func describeKeyring(armored string) []SigningKey {
	entities, err := parseKeyring(armored)
	if err != nil {
		return nil
	}
	keys := make([]SigningKey, 0, len(entities))
	for _, entity := range entities {
		if entity == nil || entity.PrimaryKey == nil {
			continue
		}
		key := SigningKey{
			Fingerprint: strings.ToUpper(hex.EncodeToString(entity.PrimaryKey.Fingerprint)),
			KeyID:       entity.PrimaryKey.KeyIdString(),
		}
		for identity := range entity.Identities {
			key.Identities = append(key.Identities, identity)
		}
		// A map iteration would otherwise reorder the identities of the same
		// key between two reads of the same repository.
		sort.Strings(key.Identities)
		keys = append(keys, key)
	}
	sort.Slice(keys, func(first, second int) bool {
		return keys[first].Fingerprint < keys[second].Fingerprint
	})
	return keys
}

// verifyChartProvenance checks one archive against one provenance document.
//
// `names` are the file names the digest table may list this archive under. A
// provenance file binds a digest to a name, so the name is part of what is
// proven and is not guessed at: the caller passes the canonical
// `<chart>-<version>.tgz` and, on the HTTP path, the basename the archive was
// actually downloaded under, which a repository is free to choose differently.
//
// An empty keyring is a refusal rather than a pass. Verifying against no keys
// would accept any well-formed signature, which is the opposite of the answer.
func verifyChartProvenance(
	keyring string,
	archive []byte,
	document []byte,
	names []string,
) (ChartSignature, error) {
	entities, err := parseKeyring(keyring)
	if err != nil {
		return ChartSignature{}, err
	}
	if len(entities) == 0 {
		return ChartSignature{}, fmt.Errorf(
			"%w: this repository has no signing keys configured",
			ErrChartSignatureInvalid,
		)
	}
	block, _ := clearsign.Decode(document)
	if block == nil {
		return ChartSignature{}, fmt.Errorf(
			"%w: provenance file holds no clear-signed block",
			ErrChartSignatureInvalid,
		)
	}
	// The signature is checked before the body is parsed. The body is a
	// document a repository handed over, and nothing in it should be read as
	// meaningful until a trusted key has vouched for it.
	signer, err := openpgp.CheckDetachedSignature(
		entities,
		bytes.NewReader(block.Bytes),
		block.ArmoredSignature.Body,
		nil,
	)
	if err != nil || signer == nil || signer.PrimaryKey == nil {
		return ChartSignature{}, fmt.Errorf(
			"%w: no configured key signed this provenance file",
			ErrChartSignatureInvalid,
		)
	}
	sums, err := provenanceSums(block.Plaintext)
	if err != nil {
		return ChartSignature{}, err
	}
	digest := "sha256:" + hex.EncodeToString(hashArchive(archive))
	for _, name := range names {
		if name == "" {
			continue
		}
		published, listed := sums.Files[name]
		if !listed {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(published), digest) {
			// The name is signed and the digest disagrees: this is the case
			// worth naming, because it is what a rewritten archive looks like.
			return ChartSignature{}, fmt.Errorf(
				"%w: %s is signed as %s but this archive is %s",
				ErrChartSignatureInvalid,
				name,
				published,
				digest,
			)
		}
		signature := ChartSignature{
			Verified: true,
			KeyID:    signer.PrimaryKey.KeyIdString(),
			Digest:   digest,
			FileName: name,
		}
		for identity := range signer.Identities {
			signature.SignedBy = append(signature.SignedBy, identity)
		}
		sort.Strings(signature.SignedBy)
		return signature, nil
	}
	return ChartSignature{}, fmt.Errorf(
		"%w: the signed digest table names none of %s",
		ErrChartSignatureInvalid,
		strings.Join(names, ", "),
	)
}

// provenanceSums reads the digest table out of a verified message body.
//
// Helm writes the body as the chart's Chart.yaml, a YAML document terminator,
// and then the table. Only the second half is read: the metadata half restates
// what the archive already contains, and treating a repository's copy of it as
// authoritative would create a second answer to "what is this chart".
func provenanceSums(plaintext []byte) (*provenance.SumCollection, error) {
	parts := bytes.SplitN(plaintext, []byte("\n...\n"), 2)
	if len(parts) < 2 {
		return nil, fmt.Errorf(
			"%w: provenance body has no digest table",
			ErrChartSignatureInvalid,
		)
	}
	sums := &provenance.SumCollection{}
	if err := yaml.Unmarshal(parts[1], sums); err != nil {
		return nil, fmt.Errorf(
			"%w: provenance digest table is not readable",
			ErrChartSignatureInvalid,
		)
	}
	if len(sums.Files) == 0 {
		return nil, fmt.Errorf(
			"%w: provenance digest table is empty",
			ErrChartSignatureInvalid,
		)
	}
	return sums, nil
}

func hashArchive(archive []byte) []byte {
	sum := sha256.Sum256(archive)
	return sum[:]
}

// provenanceFileName is the name Helm gives a packaged chart, and therefore the
// name its provenance file signs it under.
func provenanceFileName(chartName string, version string) string {
	return fmt.Sprintf("%s-%s.tgz", chartName, version)
}
