// Package helm turns a chart catalogue and a Cluster into an installed
// application.
//
// It owns three things that do not belong together anywhere else: the
// platform's chart repositories, the charts those repositories publish, and the
// release lifecycle inside one Cluster. They are one package because a release
// operation needs all three — the chart archive is fetched from a repository by
// this Server and handed to the Cluster's Agent, which is the only side that
// runs Helm.
//
// What this package does not do is render a chart or write a release Secret.
// Both need Helm's own engine, and both happen in the Agent.
package helm

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput = errors.New("invalid Helm request")
	// ErrRepositoryNotFound and ErrRepositoryConflict are the store's errors
	// restated so a handler does not have to import the store to map them.
	ErrRepositoryNotFound = store.ErrHelmRepositoryNotFound
	ErrRepositoryConflict = store.ErrHelmRepositoryConflict
	// ErrRepositoryDisabled separates "this repository is switched off" from
	// "there is no such repository": only one of them is fixed by an
	// administrator turning something back on.
	ErrRepositoryDisabled = errors.New("Helm chart repository is disabled")
)

// invalidError is a rejection whose account describes what the caller sent, so
// it is safe — and useful — to return. "invalid Helm request" tells an
// administrator filling in a form nothing at all; naming the field tells them
// what to fix.
type invalidError struct {
	detail string
}

func (err *invalidError) Error() string  { return err.detail }
func (err *invalidError) Detail() string { return err.detail }
func (err *invalidError) Unwrap() error  { return ErrInvalidInput }

func invalid(format string, arguments ...any) error {
	return &invalidError{detail: fmt.Sprintf(format, arguments...)}
}

const (
	maxRepositoryNameLength        = 100
	maxRepositoryDescriptionLength = 500
	maxRepositoryURLLength         = 2000
	maxRepositoryUsernameLength    = 200
	maxRepositoryPasswordLength    = 1000
	maxRepositoryCALength          = 65536
)

// RepositoryStore is the persistence surface this service needs.
type RepositoryStore interface {
	ListHelmRepositories(context.Context) ([]store.HelmRepository, error)
	GetHelmRepository(context.Context, string) (store.HelmRepository, error)
	GetHelmRepositoryCredentials(context.Context, string) (store.HelmRepository, error)
	CreateHelmRepository(context.Context, store.CreateHelmRepositoryParams) (store.HelmRepository, error)
	UpdateHelmRepository(context.Context, store.UpdateHelmRepositoryParams) (store.HelmRepository, error)
	DeleteHelmRepository(context.Context, string) error
}

// Repository is one catalogue entry as the API reports it. The password is not
// here and never will be: `HasCredentials` is the whole of what a reader is
// told about it.
type Repository struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	URL            string `json:"url"`
	Username       string `json:"username"`
	HasCredentials bool   `json:"has_credentials"`
	// CACertificateProvided says one is configured; CACertificatePEM is the
	// certificate itself. It is returned in full, like the signing keys and
	// unlike the password, because it is a public certificate and because an
	// edit form that cannot read it back cannot preserve it: a save would send
	// an empty field and clear a CA nobody meant to remove.
	CACertificateProvided bool   `json:"ca_certificate_provided"`
	CACertificatePEM      string `json:"ca_certificate_pem"`
	InsecureSkipTLSVerify bool   `json:"insecure_skip_tls_verify"`
	Enabled               bool   `json:"enabled"`
	// SignaturePolicy is what this repository requires of a chart's provenance.
	// See provenance.go.
	SignaturePolicy SignaturePolicy `json:"signature_policy"`
	// SigningKeys and PublicKeyring are the same keys twice, for the two things
	// that are done with them. SigningKeys is what a reader asked for —
	// fingerprints and identities answer "which keys do we trust here", and an
	// armor block does not. PublicKeyring is the stored text, returned in full
	// because an administrator replacing one key of several has to edit what is
	// there. Neither is a secret: these are public keys, and the routes that
	// return them are behind the platform administrator gate regardless.
	SigningKeys   []SigningKey `json:"signing_keys"`
	PublicKeyring string       `json:"public_keyring"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
}

type RepositoryPage struct {
	Repositories []Repository `json:"repositories"`
}

// RepositoryInput is what an administrator submits. Password is three-state on
// update — nil keeps the stored one — and is taken literally on create.
type RepositoryInput struct {
	Name                  string  `json:"name"`
	Description           string  `json:"description"`
	URL                   string  `json:"url"`
	Username              string  `json:"username"`
	Password              *string `json:"password"`
	CACertificatePEM      string  `json:"ca_certificate_pem"`
	InsecureSkipTLSVerify bool    `json:"insecure_skip_tls_verify"`
	Enabled               *bool   `json:"enabled"`
	// SignaturePolicy and PublicKeyring configure chart provenance. Unlike the
	// password the keyring is sent and returned in full — these are public
	// keys, and an administrator has to be able to read back what they pasted
	// in order to replace one of them.
	SignaturePolicy string `json:"signature_policy"`
	PublicKeyring   string `json:"public_keyring"`
}

func (service *Service) ListRepositories(ctx context.Context) (RepositoryPage, error) {
	rows, err := service.repositories.ListHelmRepositories(ctx)
	if err != nil {
		return RepositoryPage{}, err
	}
	page := RepositoryPage{Repositories: make([]Repository, 0, len(rows))}
	for _, row := range rows {
		page.Repositories = append(page.Repositories, publicRepository(row))
	}
	return page, nil
}

func (service *Service) GetRepository(ctx context.Context, id string) (Repository, error) {
	if !validation.IsUUID(id) {
		return Repository{}, ErrInvalidInput
	}
	row, err := service.repositories.GetHelmRepository(ctx, id)
	if err != nil {
		return Repository{}, err
	}
	return publicRepository(row), nil
}

func (service *Service) CreateRepository(
	ctx context.Context,
	input RepositoryInput,
	actorUserID string,
) (Repository, error) {
	normalized, err := normalizeRepositoryInput(input)
	if err != nil {
		return Repository{}, err
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return Repository{}, err
	}
	password := ""
	if input.Password != nil {
		password = *input.Password
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	row, err := service.repositories.CreateHelmRepository(ctx, store.CreateHelmRepositoryParams{
		ID:                    id,
		Name:                  normalized.Name,
		Description:           normalized.Description,
		URL:                   normalized.URL,
		Username:              normalized.Username,
		Password:              password,
		CACertificatePEM:      normalized.CACertificatePEM,
		InsecureSkipTLSVerify: input.InsecureSkipTLSVerify,
		Enabled:               enabled,
		SignaturePolicy:       string(normalized.SignaturePolicy),
		PublicKeyring:         normalized.PublicKeyring,
		ActorUserID:           actorUserID,
		Now:                   time.Now().UTC(),
	})
	if err != nil {
		return Repository{}, err
	}
	service.forgetRepository(row.ID)
	return publicRepository(row), nil
}

func (service *Service) UpdateRepository(
	ctx context.Context,
	id string,
	input RepositoryInput,
	actorUserID string,
) (Repository, error) {
	if !validation.IsUUID(id) {
		return Repository{}, ErrInvalidInput
	}
	normalized, err := normalizeRepositoryInput(input)
	if err != nil {
		return Repository{}, err
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	// Read before writing, so that what is thrown away afterwards can be
	// decided from what actually changed. A read that fails is not a reason to
	// refuse the edit — it is a reason to assume the worst below.
	before, beforeErr := service.repositories.GetHelmRepository(ctx, id)
	row, err := service.repositories.UpdateHelmRepository(ctx, store.UpdateHelmRepositoryParams{
		ID:                    id,
		Name:                  normalized.Name,
		Description:           normalized.Description,
		URL:                   normalized.URL,
		Username:              normalized.Username,
		Password:              input.Password,
		CACertificatePEM:      normalized.CACertificatePEM,
		InsecureSkipTLSVerify: input.InsecureSkipTLSVerify,
		Enabled:               enabled,
		SignaturePolicy:       string(normalized.SignaturePolicy),
		PublicKeyring:         normalized.PublicKeyring,
		ActorUserID:           actorUserID,
		Now:                   time.Now().UTC(),
	})
	if err != nil {
		return Repository{}, err
	}
	// Only an edit that could change what this repository serves, or the terms
	// on which it was read, invalidates what was cached from it.
	//
	// Forgetting on every edit was simpler and was wrong in one direction that
	// matters: a public index is tens of megabytes and the archives behind it
	// far more, and renaming an entry or switching it off for an afternoon
	// threw all of it away and made everyone who opened the catalogue next pay
	// to fetch it again. A name is not part of what a repository publishes.
	//
	// The judgement is deliberately one-sided. Everything that decides which
	// bytes arrive, or whether they were to be trusted, is here; anything this
	// cannot establish — including a read that failed — forgets.
	if beforeErr != nil || repositorySourceChanged(before, row, input.Password != nil) {
		service.forgetRepository(id)
	}
	return publicRepository(row), nil
}

// repositorySourceChanged reports whether an edit changed where a repository is
// read from, who it is read as, or what its answers had to prove.
//
// A supplied password counts whether or not it differs from the stored one: the
// stored value is never read back, so "the same password" is not a comparison
// this Server can make, and treating a submitted one as a change costs a
// re-fetch rather than a wrong answer.
func repositorySourceChanged(
	before store.HelmRepository,
	after store.HelmRepository,
	passwordSupplied bool,
) bool {
	return passwordSupplied ||
		before.URL != after.URL ||
		before.Username != after.Username ||
		before.CACertificatePEM != after.CACertificatePEM ||
		before.InsecureSkipTLSVerify != after.InsecureSkipTLSVerify ||
		// A signing policy decides whether an archive was allowed to be here at
		// all, and the keyring decides what "verified" meant when it was
		// written — see chartMeta.ProvenanceChecked, which relies on this.
		before.SignaturePolicy != after.SignaturePolicy ||
		before.PublicKeyring != after.PublicKeyring
}

func (service *Service) DeleteRepository(ctx context.Context, id string) error {
	if !validation.IsUUID(id) {
		return ErrInvalidInput
	}
	if err := service.repositories.DeleteHelmRepository(ctx, id); err != nil {
		return err
	}
	service.forgetRepository(id)
	return nil
}

func publicRepository(row store.HelmRepository) Repository {
	return Repository{
		ID:                    row.ID,
		Name:                  row.Name,
		Description:           row.Description,
		URL:                   row.URL,
		Username:              row.Username,
		HasCredentials:        row.HasCredentials,
		CACertificateProvided: row.CACertificatePEM != "",
		CACertificatePEM:      row.CACertificatePEM,
		InsecureSkipTLSVerify: row.InsecureSkipTLSVerify,
		Enabled:               row.Enabled,
		SignaturePolicy:       storedSignaturePolicy(row.SignaturePolicy),
		SigningKeys:           describeKeyring(row.PublicKeyring),
		PublicKeyring:         row.PublicKeyring,
		CreatedAt:             row.CreatedAt,
		UpdatedAt:             row.UpdatedAt,
	}
}

type normalizedRepository struct {
	Name             string
	Description      string
	URL              string
	Username         string
	CACertificatePEM string
	SignaturePolicy  SignaturePolicy
	PublicKeyring    string
}

// normalizeRepositoryInput trims and checks what an administrator submitted.
//
// The URL is the field that matters: it is an address this Server will make
// requests to, so only http and https are accepted and the value must parse to
// an absolute URL with a host. Everything past that — which host, which network
// — is the administrator's decision, and it is theirs to make because these
// routes are behind the global administrator gate.
func normalizeRepositoryInput(input RepositoryInput) (normalizedRepository, error) {
	name := strings.TrimSpace(input.Name)
	description := strings.TrimSpace(input.Description)
	rawURL := strings.TrimSpace(input.URL)
	username := strings.TrimSpace(input.Username)
	certificate := strings.TrimSpace(input.CACertificatePEM)
	if name == "" || len(name) > maxRepositoryNameLength ||
		len(description) > maxRepositoryDescriptionLength ||
		len(username) > maxRepositoryUsernameLength ||
		len(certificate) > maxRepositoryCALength ||
		len(rawURL) > maxRepositoryURLLength {
		return normalizedRepository{}, ErrInvalidInput
	}
	if input.Password != nil && len(*input.Password) > maxRepositoryPasswordLength {
		return normalizedRepository{}, ErrInvalidInput
	}
	if certificate != "" && !strings.Contains(certificate, "-----BEGIN CERTIFICATE-----") {
		return normalizedRepository{}, invalid("CA certificate must be PEM encoded")
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") {
		return normalizedRepository{}, invalid(
			"repository URL must be an absolute http or https URL",
		)
	}
	// A credential in the URL would be stored in a column nothing redacts and
	// echoed back by every read of the catalogue.
	if parsed.User != nil {
		return normalizedRepository{}, invalid(
			"repository URL must not embed credentials; use the username and password fields",
		)
	}
	policy, keyring, err := normalizeRepositoryProvenance(input)
	if err != nil {
		return normalizedRepository{}, err
	}
	return normalizedRepository{
		Name:             name,
		Description:      description,
		URL:              strings.TrimRight(parsed.String(), "/"),
		Username:         username,
		CACertificatePEM: certificate,
		SignaturePolicy:  policy,
		PublicKeyring:    keyring,
	}, nil
}

// normalizeRepositoryProvenance checks the signing policy and the keys it is to
// be enforced with, together.
//
// They are validated as a pair because neither is meaningful alone: a policy
// with no keys refuses every chart under `required` and — worse — admits every
// chart under `verify_if_present`, which would read on the page as verification
// while checking nothing. Keys with no policy are harmless and are kept, so an
// administrator can load the keyring first and switch the policy on afterwards.
//
// The keyring is parsed here rather than only at install time. A key that does
// not decode is a mistake somebody is in a position to correct while they are
// looking at the form; discovering it during a deployment is not.
func normalizeRepositoryProvenance(input RepositoryInput) (SignaturePolicy, string, error) {
	policy, err := normalizeSignaturePolicy(input.SignaturePolicy)
	if err != nil {
		return "", "", err
	}
	keyring := strings.TrimSpace(input.PublicKeyring)
	if keyring != "" {
		if _, err := parseKeyring(keyring); err != nil {
			return "", "", err
		}
	}
	if policy != SignatureDisabled && keyring == "" {
		return "", "", invalid(
			"signature policy %q requires at least one PGP public key",
			policy,
		)
	}
	return policy, keyring, nil
}
