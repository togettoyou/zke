package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrHelmRepositoryNotFound = errors.New("Helm chart repository not found")
	ErrHelmRepositoryConflict = errors.New("Helm chart repository name is already in use")
)

// HelmRepositoryStore is the platform-wide chart catalogue.
//
// It stores where charts come from and nothing about the charts themselves: an
// index document and a chart archive are fetched when they are needed and held
// in memory, so the catalogue never becomes a second artifact store that has to
// be backed up, garbage collected and kept consistent with its upstream.
type HelmRepositoryStore struct {
	pool *pgxpool.Pool
}

func NewHelmRepositoryStore(pool *pgxpool.Pool) *HelmRepositoryStore {
	return &HelmRepositoryStore{pool: pool}
}

// HelmRepository is one catalogue entry.
//
// Password is populated only by the internal read the Server uses when it is
// about to make a request upstream. Every API-facing read goes through
// ListHelmRepositories or GetHelmRepository, which leave it empty — the value
// exists to be sent to the repository, never to be shown to anyone.
type HelmRepository struct {
	ID                    string
	Name                  string
	Description           string
	URL                   string
	Username              string
	Password              string
	CACertificatePEM      string
	InsecureSkipTLSVerify bool
	Enabled               bool
	// SignaturePolicy is what this repository requires of a chart's provenance:
	// nothing, a valid signature when one is published, or a valid signature
	// always. PublicKeyring holds the ASCII-armored PGP public keys it is
	// verified against — public material, so unlike the password it is read
	// back, and the API turns it into identities rather than echoing the armor.
	SignaturePolicy string
	PublicKeyring   string
	// HasCredentials reports whether a password is stored, without reporting
	// what it is. It is what a Console needs in order to say "authenticated"
	// and to offer "replace the password" rather than "set one".
	HasCredentials  bool
	CreatedByUserID string
	UpdatedByUserID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// The public projection. `password` is deliberately absent and replaced by the
// fact of its presence: a column that is never selected cannot be leaked by a
// handler that forgets to clear it.
const helmRepositoryColumns = `
    id::text,
    name,
    description,
    url,
    username,
    ca_certificate_pem,
    insecure_skip_tls_verify,
    enabled,
    signature_policy,
    public_keyring,
    password <> '',
    COALESCE(created_by_user_id::text, ''),
    COALESCE(updated_by_user_id::text, ''),
    created_at,
    updated_at`

func scanHelmRepository(row pgx.Row) (HelmRepository, error) {
	var repository HelmRepository
	err := row.Scan(
		&repository.ID,
		&repository.Name,
		&repository.Description,
		&repository.URL,
		&repository.Username,
		&repository.CACertificatePEM,
		&repository.InsecureSkipTLSVerify,
		&repository.Enabled,
		&repository.SignaturePolicy,
		&repository.PublicKeyring,
		&repository.HasCredentials,
		&repository.CreatedByUserID,
		&repository.UpdatedByUserID,
		&repository.CreatedAt,
		&repository.UpdatedAt,
	)
	return repository, err
}

func (store *HelmRepositoryStore) ListHelmRepositories(
	ctx context.Context,
) ([]HelmRepository, error) {
	rows, err := store.pool.Query(ctx, `SELECT `+helmRepositoryColumns+`
FROM helm_repositories
ORDER BY lower(name), id`)
	if err != nil {
		return nil, fmt.Errorf("list Helm chart repositories: %w", err)
	}
	defer rows.Close()
	repositories := make([]HelmRepository, 0)
	for rows.Next() {
		repository, err := scanHelmRepository(rows)
		if err != nil {
			return nil, fmt.Errorf("scan Helm chart repository: %w", err)
		}
		repositories = append(repositories, repository)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Helm chart repositories: %w", err)
	}
	return repositories, nil
}

func (store *HelmRepositoryStore) GetHelmRepository(
	ctx context.Context,
	id string,
) (HelmRepository, error) {
	repository, err := scanHelmRepository(store.pool.QueryRow(ctx,
		`SELECT `+helmRepositoryColumns+` FROM helm_repositories WHERE id = $1`,
		id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return HelmRepository{}, ErrHelmRepositoryNotFound
	}
	if err != nil {
		return HelmRepository{}, fmt.Errorf("get Helm chart repository: %w", err)
	}
	return repository, nil
}

// GetHelmRepositoryCredentials reads one repository *with* its password, for
// the Server's own use when it is about to make a request upstream.
//
// It is a separate method rather than a flag on the read above so that every
// call site that obtains a credential is visible in a grep for this name.
func (store *HelmRepositoryStore) GetHelmRepositoryCredentials(
	ctx context.Context,
	id string,
) (HelmRepository, error) {
	repository, err := scanHelmRepository(store.pool.QueryRow(ctx,
		`SELECT `+helmRepositoryColumns+` FROM helm_repositories WHERE id = $1`,
		id,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return HelmRepository{}, ErrHelmRepositoryNotFound
	}
	if err != nil {
		return HelmRepository{}, fmt.Errorf("get Helm chart repository: %w", err)
	}
	if err := store.pool.QueryRow(ctx,
		`SELECT password FROM helm_repositories WHERE id = $1`, id,
	).Scan(&repository.Password); err != nil {
		return HelmRepository{}, fmt.Errorf("read Helm chart repository credential: %w", err)
	}
	return repository, nil
}

type CreateHelmRepositoryParams struct {
	ID                    string
	Name                  string
	Description           string
	URL                   string
	Username              string
	Password              string
	CACertificatePEM      string
	InsecureSkipTLSVerify bool
	Enabled               bool
	SignaturePolicy       string
	PublicKeyring         string
	ActorUserID           string
	Now                   time.Time
}

func (store *HelmRepositoryStore) CreateHelmRepository(
	ctx context.Context,
	input CreateHelmRepositoryParams,
) (HelmRepository, error) {
	repository, err := scanHelmRepository(store.pool.QueryRow(ctx, `
INSERT INTO helm_repositories (
    id, name, description, url, username, password,
    ca_certificate_pem, insecure_skip_tls_verify, enabled,
    signature_policy, public_keyring,
    created_by_user_id, updated_by_user_id, created_at, updated_at
) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12, $13, $13)
RETURNING `+helmRepositoryColumns,
		input.ID,
		input.Name,
		input.Description,
		input.URL,
		input.Username,
		input.Password,
		input.CACertificatePEM,
		input.InsecureSkipTLSVerify,
		input.Enabled,
		input.SignaturePolicy,
		input.PublicKeyring,
		input.ActorUserID,
		input.Now,
	))
	if isUniqueViolation(err) {
		return HelmRepository{}, ErrHelmRepositoryConflict
	}
	if err != nil {
		return HelmRepository{}, fmt.Errorf("create Helm chart repository: %w", err)
	}
	return repository, nil
}

// UpdateHelmRepositoryParams carries the whole row except the password, which
// is three-state: nil keeps what is stored, an empty string clears it, and a
// value replaces it. A Console that is never shown the password can therefore
// still save the rest of the row without erasing the credential.
type UpdateHelmRepositoryParams struct {
	ID                    string
	Name                  string
	Description           string
	URL                   string
	Username              string
	Password              *string
	CACertificatePEM      string
	InsecureSkipTLSVerify bool
	Enabled               bool
	SignaturePolicy       string
	PublicKeyring         string
	ActorUserID           string
	Now                   time.Time
}

func (store *HelmRepositoryStore) UpdateHelmRepository(
	ctx context.Context,
	input UpdateHelmRepositoryParams,
) (HelmRepository, error) {
	repository, err := scanHelmRepository(store.pool.QueryRow(ctx, `
UPDATE helm_repositories
SET name = $2,
    description = $3,
    url = $4,
    username = $5,
    password = COALESCE($6, password),
    ca_certificate_pem = $7,
    insecure_skip_tls_verify = $8,
    enabled = $9,
    signature_policy = $10,
    public_keyring = $11,
    updated_by_user_id = $12,
    updated_at = $13
WHERE id = $1
RETURNING `+helmRepositoryColumns,
		input.ID,
		input.Name,
		input.Description,
		input.URL,
		input.Username,
		input.Password,
		input.CACertificatePEM,
		input.InsecureSkipTLSVerify,
		input.Enabled,
		input.SignaturePolicy,
		input.PublicKeyring,
		input.ActorUserID,
		input.Now,
	))
	if isUniqueViolation(err) {
		return HelmRepository{}, ErrHelmRepositoryConflict
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return HelmRepository{}, ErrHelmRepositoryNotFound
	}
	if err != nil {
		return HelmRepository{}, fmt.Errorf("update Helm chart repository: %w", err)
	}
	return repository, nil
}

// DeleteHelmRepository removes a catalogue entry.
//
// Nothing references it: a release carries the chart it was installed from, so
// removing the repository it came from leaves every installed release intact
// and only stops new installs choosing from it.
func (store *HelmRepositoryStore) DeleteHelmRepository(
	ctx context.Context,
	id string,
) error {
	command, err := store.pool.Exec(ctx,
		`DELETE FROM helm_repositories WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete Helm chart repository: %w", err)
	}
	if command.RowsAffected() == 0 {
		return ErrHelmRepositoryNotFound
	}
	return nil
}
