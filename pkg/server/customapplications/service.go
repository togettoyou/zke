// Package customapplications manages Project-scoped entries shown as
// applications on the ZKE desktop.
package customapplications

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
	ErrInvalidInput        = errors.New("invalid custom application")
	ErrNotFound            = errors.New("custom application not found")
	ErrConflict            = errors.New("custom application name is already in use")
	ErrLimit               = errors.New("custom application limit reached")
	ErrIdempotencyConflict = errors.New("idempotency key was reused with different custom application input")
)

const MaxPerProject = store.MaxCustomApplicationsPerProject

const (
	maxNameBytes        = 80
	maxDescriptionBytes = 500
	maxURLBytes         = 2048
)

type InputError struct{ reason string }

func (err *InputError) Error() string  { return ErrInvalidInput.Error() + ": " + err.reason }
func (err *InputError) Unwrap() error  { return ErrInvalidInput }
func (err *InputError) Detail() string { return err.reason }

func invalid(format string, arguments ...any) error {
	return &InputError{reason: fmt.Sprintf(format, arguments...)}
}

type Store interface {
	ListCustomApplications(context.Context, string) ([]store.CustomApplication, error)
	GetCustomApplication(context.Context, string, string) (store.CustomApplication, error)
	CreateCustomApplication(context.Context, store.CreateCustomApplicationParams) (store.CustomApplication, error)
	UpdateCustomApplication(context.Context, store.UpdateCustomApplicationParams) (store.CustomApplication, error)
	DeleteCustomApplication(context.Context, string, string) error
}

type Application struct {
	ID          string
	ProjectID   string
	Name        string
	Description string
	URL         string
	LogoURL     string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

type Input struct {
	Name           string
	Description    string
	URL            string
	LogoURL        string
	IdempotencyKey string
}

type Service struct{ store Store }

func NewService(applicationStore Store) (*Service, error) {
	if applicationStore == nil {
		return nil, errors.New("custom application store is required")
	}
	return &Service{store: applicationStore}, nil
}

func (service *Service) List(ctx context.Context, projectID string) ([]Application, error) {
	if !validation.IsUUID(projectID) {
		return nil, ErrInvalidInput
	}
	rows, err := service.store.ListCustomApplications(ctx, projectID)
	if err != nil {
		return nil, err
	}
	items := make([]Application, 0, len(rows))
	for _, row := range rows {
		items = append(items, present(row))
	}
	return items, nil
}

func (service *Service) Get(ctx context.Context, projectID, id string) (Application, error) {
	if !validation.IsUUID(projectID) || !validation.IsUUID(id) {
		return Application{}, ErrInvalidInput
	}
	row, err := service.store.GetCustomApplication(ctx, projectID, id)
	if err != nil {
		return Application{}, translate(err)
	}
	return present(row), nil
}

func (service *Service) Create(
	ctx context.Context,
	projectID string,
	actorUserID string,
	input Input,
) (Application, error) {
	normalized, err := normalize(input, true)
	if err != nil {
		return Application{}, err
	}
	if !validation.IsUUID(projectID) || !validation.IsUUID(actorUserID) {
		return Application{}, ErrInvalidInput
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return Application{}, err
	}
	row, err := service.store.CreateCustomApplication(ctx, store.CreateCustomApplicationParams{
		ID: id, ProjectID: projectID, CreatedByUserID: actorUserID,
		Name: normalized.Name, Description: normalized.Description,
		URL: normalized.URL, LogoURL: normalized.LogoURL,
		IdempotencyKey: normalized.IdempotencyKey, Now: time.Now().UTC(),
	})
	if err != nil {
		return Application{}, translate(err)
	}
	return present(row), nil
}

func (service *Service) Update(
	ctx context.Context,
	projectID string,
	id string,
	input Input,
) (Application, error) {
	normalized, err := normalize(input, false)
	if err != nil {
		return Application{}, err
	}
	if !validation.IsUUID(projectID) || !validation.IsUUID(id) {
		return Application{}, ErrInvalidInput
	}
	row, err := service.store.UpdateCustomApplication(ctx, store.UpdateCustomApplicationParams{
		ID: id, ProjectID: projectID, Name: normalized.Name,
		Description: normalized.Description, URL: normalized.URL,
		LogoURL: normalized.LogoURL, Now: time.Now().UTC(),
	})
	if err != nil {
		return Application{}, translate(err)
	}
	return present(row), nil
}

func (service *Service) Delete(ctx context.Context, projectID, id string) (Application, error) {
	if !validation.IsUUID(projectID) || !validation.IsUUID(id) {
		return Application{}, ErrInvalidInput
	}
	existing, err := service.store.GetCustomApplication(ctx, projectID, id)
	if err != nil {
		return Application{}, translate(err)
	}
	if err := service.store.DeleteCustomApplication(ctx, projectID, id); err != nil {
		return Application{}, translate(err)
	}
	return present(existing), nil
}

func normalize(input Input, requireIdempotency bool) (Input, error) {
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	input.URL = strings.TrimSpace(input.URL)
	input.LogoURL = strings.TrimSpace(input.LogoURL)
	input.IdempotencyKey = strings.TrimSpace(input.IdempotencyKey)
	if len(input.Name) == 0 || len(input.Name) > maxNameBytes {
		return Input{}, invalid("应用名称必须为 1–%d 字节", maxNameBytes)
	}
	if len(input.Description) > maxDescriptionBytes {
		return Input{}, invalid("应用说明不能超过 %d 字节", maxDescriptionBytes)
	}
	if err := validateHTTPURL(input.URL, "应用 URL", false); err != nil {
		return Input{}, err
	}
	if err := validateHTTPURL(input.LogoURL, "Logo URL", true); err != nil {
		return Input{}, err
	}
	if requireIdempotency && !validation.IsIdempotencyKey(input.IdempotencyKey) {
		return Input{}, invalid("Idempotency-Key 必须为 16–128 个字符")
	}
	return input, nil
}

func validateHTTPURL(raw string, label string, optional bool) error {
	if raw == "" && optional {
		return nil
	}
	if len(raw) == 0 || len(raw) > maxURLBytes {
		return invalid("%s 必须为 1–%d 字节", label, maxURLBytes)
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return invalid("%s 必须是无用户凭证的绝对 HTTP(S) 地址", label)
	}
	return nil
}

func present(row store.CustomApplication) Application {
	return Application{
		ID: row.ID, ProjectID: row.ProjectID, Name: row.Name,
		Description: row.Description, URL: row.URL, LogoURL: row.LogoURL,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt,
	}
}

func translate(err error) error {
	switch {
	case errors.Is(err, store.ErrCustomApplicationNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrCustomApplicationConflict):
		return ErrConflict
	case errors.Is(err, store.ErrCustomApplicationLimit):
		return ErrLimit
	case errors.Is(err, store.ErrCustomApplicationIdempotencyConflict):
		return ErrIdempotencyConflict
	default:
		return err
	}
}
