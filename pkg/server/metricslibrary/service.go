// Package metricslibrary keeps the named MetricsQL expressions a Project has
// collected.
//
// Explore is where an operator writes an expression; this is where the good
// ones stop being retyped. An entry is a name, a description and the expression
// text — no Cluster, no time range, no permission. Which Cluster it describes is
// decided when somebody runs it, by the target they have selected and by the
// scope filter the Server rewrites into every selector, so an expression saved
// by one person and run by another always describes a Cluster the reader was
// already allowed to read.
//
// That is what makes sharing one safe, and it is why the permissions here are
// about curation rather than about access. Anyone who may read metrics in the
// Project keeps their own private entries and reads the shared ones; putting an
// entry into the shared list, or changing and removing one that is already
// there, needs `cluster.metrics.manage` — the permission that already means
// "decides how this Project's metrics work".
package metricslibrary

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/togettoyou/zke/pkg/server/metricsingest"
	"github.com/togettoyou/zke/pkg/server/metricsqlguard"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/identifier"
	"github.com/togettoyou/zke/pkg/shared/validation"
)

var (
	ErrInvalidInput = errors.New("invalid saved metrics query")
	ErrNotFound     = errors.New("saved metrics query not found")
	ErrConflict     = errors.New("saved metrics query name is already in use")
	ErrDenied       = errors.New("saved metrics query is not writable by this caller")
	ErrLimit        = errors.New("saved metrics query limit reached")
)

const (
	// VisibilityPrivate is visible to its author alone.
	VisibilityPrivate = "private"
	// VisibilityProject is visible to everyone who may read metrics in the
	// Project.
	VisibilityProject = "project"

	maxNameBytes        = 100
	maxDescriptionBytes = 500
)

// MaxPerProject is how many entries one Project may hold, re-exported from the
// store so the API can report the ceiling without the HTTP layer reaching into
// the database package for a number.
const MaxPerProject = store.MaxSavedMetricsQueriesPerProject

// InputError is a refusal that already says what is wrong with the submission.
//
// Detail is what the HTTP layer returns in place of its own fixed message: an
// editor that is told only "invalid request" leaves the author guessing which
// of four fields it meant. Everything in it describes what the caller just
// sent — a length, a visibility value, the storage's own account of their
// expression — and never anything about this Server or a Cluster.
type InputError struct {
	reason string
}

func (err *InputError) Error() string {
	return ErrInvalidInput.Error() + ": " + err.reason
}

func (err *InputError) Unwrap() error { return ErrInvalidInput }

func (err *InputError) Detail() string { return err.reason }

func invalid(format string, arguments ...any) error {
	return &InputError{reason: fmt.Sprintf(format, arguments...)}
}

// Store is the persistence this service needs, named here so the service can be
// tested without a database.
type Store interface {
	ListSavedMetricsQueries(ctx context.Context, projectID, readerUserID string) (
		[]store.SavedMetricsQuery, error,
	)
	GetSavedMetricsQuery(ctx context.Context, projectID, id string) (
		store.SavedMetricsQuery, error,
	)
	CreateSavedMetricsQuery(ctx context.Context, input store.CreateSavedMetricsQueryParams) (
		store.SavedMetricsQuery, error,
	)
	UpdateSavedMetricsQuery(ctx context.Context, input store.UpdateSavedMetricsQueryParams) (
		store.SavedMetricsQuery, error,
	)
	DeleteSavedMetricsQuery(ctx context.Context, projectID, id string) error
}

// CurationAuthorizer answers the one question this service asks beyond what the
// route already checked: may this caller change what the Project shares?
type CurationAuthorizer interface {
	AuthorizeProject(
		ctx context.Context,
		userID string,
		permission rbac.Permission,
		projectID string,
	) (rbac.ResolvedScope, error)
}

// RBACCuration adapts the RBAC service. The permission is fixed here rather
// than passed in, so the boundary is stated in code and not in an argument.
type RBACCuration struct {
	Service *rbac.Service
}

func (adapter RBACCuration) AuthorizeProject(
	ctx context.Context,
	userID string,
	permission rbac.Permission,
	projectID string,
) (rbac.ResolvedScope, error) {
	return adapter.Service.AuthorizeProject(ctx, userID, permission, projectID)
}

// SavedQuery is one entry as the API returns it.
type SavedQuery struct {
	ID               string
	ProjectID        string
	OwnerUserID      string
	OwnerDisplayName string
	Visibility       string
	Name             string
	Description      string
	Expression       string
	// Editable says whether the caller reading this list may change or remove
	// this entry. It is derived from the same rules the write paths enforce, so
	// the Console can hide an action it would only be refused for — and the
	// Server still refuses it either way.
	Editable  bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

type Input struct {
	Name        string
	Description string
	Expression  string
	Visibility  string
}

type Service struct {
	store         Store
	authorization CurationAuthorizer
}

func NewService(
	savedQueries Store,
	authorization CurationAuthorizer,
) (*Service, error) {
	if savedQueries == nil || authorization == nil {
		return nil, errors.New("saved metrics query dependencies are required")
	}
	return &Service{store: savedQueries, authorization: authorization}, nil
}

// List returns the Project's shared entries together with the caller's own.
func (service *Service) List(
	ctx context.Context,
	projectID string,
	userID string,
) ([]SavedQuery, error) {
	if !validation.IsUUID(projectID) || !validation.IsUUID(userID) {
		return nil, ErrInvalidInput
	}
	rows, err := service.store.ListSavedMetricsQueries(ctx, projectID, userID)
	if err != nil {
		return nil, err
	}
	// One authorization check for the whole list rather than one per row: the
	// answer is a property of the caller and the Project, and asking it once
	// per entry would make a picker's cost grow with the library.
	curator := service.mayCurate(ctx, userID, projectID)
	items := make([]SavedQuery, 0, len(rows))
	for _, row := range rows {
		items = append(items, present(row, userID, curator))
	}
	return items, nil
}

// Create stores a new entry.
func (service *Service) Create(
	ctx context.Context,
	projectID string,
	userID string,
	input Input,
) (SavedQuery, error) {
	normalized, err := normalize(input)
	if err != nil {
		return SavedQuery{}, err
	}
	if !validation.IsUUID(projectID) || !validation.IsUUID(userID) {
		return SavedQuery{}, ErrInvalidInput
	}
	if normalized.Visibility == VisibilityProject {
		if err := service.requireCuration(ctx, userID, projectID); err != nil {
			return SavedQuery{}, err
		}
	}
	id, err := identifier.NewUUID()
	if err != nil {
		return SavedQuery{}, err
	}
	row, err := service.store.CreateSavedMetricsQuery(
		ctx,
		store.CreateSavedMetricsQueryParams{
			ID:          id,
			ProjectID:   projectID,
			OwnerUserID: userID,
			Visibility:  normalized.Visibility,
			Name:        normalized.Name,
			Description: normalized.Description,
			Expression:  normalized.Expression,
			Now:         time.Now().UTC(),
		},
	)
	if err != nil {
		return SavedQuery{}, translate(err)
	}
	return present(row, userID, service.mayCurate(ctx, userID, projectID)), nil
}

// Update rewrites one entry.
//
// Both the state it is in and the state it is moving to have to be writable by
// the caller. Checking only the target would let somebody with no curation
// permission edit a shared entry by also making it private in the same request;
// checking only the current state would let them publish one.
func (service *Service) Update(
	ctx context.Context,
	projectID string,
	userID string,
	id string,
	input Input,
) (SavedQuery, error) {
	normalized, err := normalize(input)
	if err != nil {
		return SavedQuery{}, err
	}
	if !validation.IsUUID(projectID) || !validation.IsUUID(userID) || !validation.IsUUID(id) {
		return SavedQuery{}, ErrInvalidInput
	}
	existing, err := service.store.GetSavedMetricsQuery(ctx, projectID, id)
	if err != nil {
		return SavedQuery{}, translate(err)
	}
	if err := service.requireWritable(ctx, userID, projectID, existing.Visibility, existing.OwnerUserID); err != nil {
		return SavedQuery{}, err
	}
	if normalized.Visibility != existing.Visibility {
		if err := service.requireWritable(
			ctx, userID, projectID, normalized.Visibility, existing.OwnerUserID,
		); err != nil {
			return SavedQuery{}, err
		}
	}
	row, err := service.store.UpdateSavedMetricsQuery(
		ctx,
		store.UpdateSavedMetricsQueryParams{
			ID:          id,
			ProjectID:   projectID,
			Visibility:  normalized.Visibility,
			Name:        normalized.Name,
			Description: normalized.Description,
			Expression:  normalized.Expression,
			Now:         time.Now().UTC(),
		},
	)
	if err != nil {
		return SavedQuery{}, translate(err)
	}
	return present(row, userID, service.mayCurate(ctx, userID, projectID)), nil
}

func (service *Service) Delete(
	ctx context.Context,
	projectID string,
	userID string,
	id string,
) (SavedQuery, error) {
	if !validation.IsUUID(projectID) || !validation.IsUUID(userID) || !validation.IsUUID(id) {
		return SavedQuery{}, ErrInvalidInput
	}
	existing, err := service.store.GetSavedMetricsQuery(ctx, projectID, id)
	if err != nil {
		return SavedQuery{}, translate(err)
	}
	if err := service.requireWritable(
		ctx, userID, projectID, existing.Visibility, existing.OwnerUserID,
	); err != nil {
		return SavedQuery{}, err
	}
	if err := service.store.DeleteSavedMetricsQuery(ctx, projectID, id); err != nil {
		return SavedQuery{}, translate(err)
	}
	return present(existing, userID, true), nil
}

// requireWritable applies the two rules that decide who may change an entry: a
// private one belongs to its author and nobody else, a shared one belongs to
// whoever curates the Project's metrics.
func (service *Service) requireWritable(
	ctx context.Context,
	userID string,
	projectID string,
	visibility string,
	ownerUserID string,
) error {
	if visibility == VisibilityProject {
		return service.requireCuration(ctx, userID, projectID)
	}
	if ownerUserID != userID {
		// Deliberately the same refusal a caller gets for an entry that does
		// not exist would be wrong here: the route already authorized this
		// Project, and the list this caller reads never contained the entry,
		// so there is nothing to conceal by conflating the two.
		return ErrDenied
	}
	return nil
}

func (service *Service) requireCuration(
	ctx context.Context,
	userID string,
	projectID string,
) error {
	_, err := service.authorization.AuthorizeProject(
		ctx,
		userID,
		rbac.PermissionClusterMetricsManage,
		projectID,
	)
	if errors.Is(err, rbac.ErrDenied) {
		return ErrDenied
	}
	if err != nil {
		return err
	}
	return nil
}

// mayCurate answers the same question for a listing, where a failure is not
// worth failing the request over: the worst it costs is an edit button that is
// not offered, and the write path decides for real.
func (service *Service) mayCurate(
	ctx context.Context,
	userID string,
	projectID string,
) bool {
	return service.requireCuration(ctx, userID, projectID) == nil
}

func present(row store.SavedMetricsQuery, readerUserID string, curator bool) SavedQuery {
	editable := curator
	if row.Visibility == VisibilityPrivate {
		editable = row.OwnerUserID == readerUserID
	}
	return SavedQuery{
		ID:               row.ID,
		ProjectID:        row.ProjectID,
		OwnerUserID:      row.OwnerUserID,
		OwnerDisplayName: row.OwnerDisplayName,
		Visibility:       row.Visibility,
		Name:             row.Name,
		Description:      row.Description,
		Expression:       row.Expression,
		Editable:         editable,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
}

type normalized struct {
	Name        string
	Description string
	Expression  string
	Visibility  string
}

// normalize trims and checks what was submitted.
//
// The expression goes through the same guard that will rewrite it at query
// time, against a placeholder target. Storing one the guard would refuse means
// the failure surfaces the first time somebody runs it — from the picker,
// where nothing explains it — instead of when it was written, where it can be
// corrected.
func normalize(input Input) (normalized, error) {
	name := strings.TrimSpace(input.Name)
	description := strings.TrimSpace(input.Description)
	expression := strings.TrimSpace(input.Expression)
	if name == "" || len(name) > maxNameBytes {
		return normalized{}, invalid("名称不能为空，且不超过 %d 字节", maxNameBytes)
	}
	if hasControlCharacters(name) || hasControlCharacters(description) {
		return normalized{}, invalid("名称与说明不能包含控制字符")
	}
	if len(description) > maxDescriptionBytes {
		return normalized{}, invalid("说明不超过 %d 字节", maxDescriptionBytes)
	}
	if input.Visibility != VisibilityPrivate && input.Visibility != VisibilityProject {
		return normalized{}, invalid("可见范围必须是 private 或 project")
	}
	if err := metricsqlguard.Validate(expression, metricsingest.ClusterLabel); err != nil {
		return normalized{}, invalid("%s", guardReason(err))
	}
	return normalized{
		Name:        name,
		Description: description,
		Expression:  expression,
		Visibility:  input.Visibility,
	}, nil
}

// hasControlCharacters keeps a name out of the picker that would break the line
// it is rendered on. Newlines and tabs included: a name is one line.
func hasControlCharacters(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

// guardReason renders a guard refusal for the author. A syntax error carries
// VictoriaMetrics' own message, which quotes what was typed; an unsupported
// expression carries this Server's reason. Both describe the submitted text.
func guardReason(err error) string {
	var syntax *metricsqlguard.SyntaxError
	if errors.As(err, &syntax) {
		return syntax.Error()
	}
	if errors.Is(err, metricsqlguard.ErrUnsupported) {
		if _, reason, found := strings.Cut(err.Error(), ": "); found {
			return reason
		}
	}
	return "表达式无法执行"
}

func translate(err error) error {
	switch {
	case errors.Is(err, store.ErrSavedMetricsQueryNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrSavedMetricsQueryConflict):
		return ErrConflict
	case errors.Is(err, store.ErrSavedMetricsQueryLimit):
		return ErrLimit
	default:
		return err
	}
}
