package metricslibrary

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	projectID  = "00000000-0000-4000-8000-000000000001"
	authorID   = "00000000-0000-4000-8000-000000000002"
	strangerID = "00000000-0000-4000-8000-000000000003"
	entryID    = "00000000-0000-4000-8000-000000000004"
)

type stubStore struct {
	rows    []store.SavedMetricsQuery
	created store.CreateSavedMetricsQueryParams
	updated store.UpdateSavedMetricsQueryParams
	deleted string
	err     error
}

func (stub *stubStore) ListSavedMetricsQueries(
	context.Context, string, string,
) ([]store.SavedMetricsQuery, error) {
	return stub.rows, stub.err
}

func (stub *stubStore) GetSavedMetricsQuery(
	_ context.Context, _ string, id string,
) (store.SavedMetricsQuery, error) {
	if stub.err != nil {
		return store.SavedMetricsQuery{}, stub.err
	}
	for _, row := range stub.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return store.SavedMetricsQuery{}, store.ErrSavedMetricsQueryNotFound
}

func (stub *stubStore) CreateSavedMetricsQuery(
	_ context.Context, input store.CreateSavedMetricsQueryParams,
) (store.SavedMetricsQuery, error) {
	stub.created = input
	if stub.err != nil {
		return store.SavedMetricsQuery{}, stub.err
	}
	return store.SavedMetricsQuery{
		ID:          input.ID,
		ProjectID:   input.ProjectID,
		OwnerUserID: input.OwnerUserID,
		Visibility:  input.Visibility,
		Name:        input.Name,
		Description: input.Description,
		Expression:  input.Expression,
	}, nil
}

func (stub *stubStore) UpdateSavedMetricsQuery(
	_ context.Context, input store.UpdateSavedMetricsQueryParams,
) (store.SavedMetricsQuery, error) {
	stub.updated = input
	if stub.err != nil {
		return store.SavedMetricsQuery{}, stub.err
	}
	return store.SavedMetricsQuery{
		ID:         input.ID,
		ProjectID:  input.ProjectID,
		Visibility: input.Visibility,
		Name:       input.Name,
		Expression: input.Expression,
	}, nil
}

func (stub *stubStore) DeleteSavedMetricsQuery(
	_ context.Context, _ string, id string,
) error {
	stub.deleted = id
	return stub.err
}

type stubCuration struct {
	allowed bool
}

func (stub stubCuration) AuthorizeProject(
	context.Context, string, rbac.Permission, string,
) (rbac.ResolvedScope, error) {
	if stub.allowed {
		return rbac.ResolvedScope{ProjectID: projectID}, nil
	}
	return rbac.ResolvedScope{}, rbac.ErrDenied
}

func newService(t *testing.T, savedQueries Store, curator bool) *Service {
	t.Helper()
	service, err := NewService(savedQueries, stubCuration{allowed: curator})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service
}

func validInput() Input {
	return Input{
		Name:       "内存用量",
		Expression: `sum by (node) (node_memory_working_set_bytes)`,
		Visibility: VisibilityPrivate,
	}
}

// The permission split: keeping a query for yourself needs nothing beyond the
// read permission the route already checked; putting one into the Project's
// shared list is curation.
func TestSharingRequiresCuration(t *testing.T) {
	t.Parallel()

	shared := validInput()
	shared.Visibility = VisibilityProject

	reader := newService(t, &stubStore{}, false)
	if _, err := reader.Create(context.Background(), projectID, authorID, shared); !errors.Is(err, ErrDenied) {
		t.Fatalf("Create(shared) without curation error = %v, want ErrDenied", err)
	}
	if _, err := reader.Create(context.Background(), projectID, authorID, validInput()); err != nil {
		t.Fatalf("Create(private) without curation error = %v, want nil", err)
	}

	curator := newService(t, &stubStore{}, true)
	if _, err := curator.Create(context.Background(), projectID, authorID, shared); err != nil {
		t.Fatalf("Create(shared) with curation error = %v, want nil", err)
	}
}

func TestPrivateEntriesAreWritableOnlyByTheirAuthor(t *testing.T) {
	t.Parallel()

	rows := []store.SavedMetricsQuery{{
		ID:          entryID,
		ProjectID:   projectID,
		OwnerUserID: authorID,
		Visibility:  VisibilityPrivate,
		Name:        "内存用量",
		Expression:  "up",
	}}

	// Curation does not help: a private entry is not part of the Project's
	// shared library, so there is nothing there to curate.
	curator := newService(t, &stubStore{rows: rows}, true)
	if _, err := curator.Update(
		context.Background(), projectID, strangerID, entryID, validInput(),
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("Update(somebody else's private) error = %v, want ErrDenied", err)
	}
	if _, err := curator.Delete(
		context.Background(), projectID, strangerID, entryID,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("Delete(somebody else's private) error = %v, want ErrDenied", err)
	}
	if _, err := curator.Update(
		context.Background(), projectID, authorID, entryID, validInput(),
	); err != nil {
		t.Fatalf("Update(own private) error = %v, want nil", err)
	}
}

// Both ends of a visibility change have to be writable. Checking only the
// target state would let a reader publish their private entry by naming
// `project`, and checking only the current state would let them edit a shared
// entry by making it private in the same request.
func TestChangingVisibilityNeedsBothStates(t *testing.T) {
	t.Parallel()

	shared := []store.SavedMetricsQuery{{
		ID:          entryID,
		ProjectID:   projectID,
		OwnerUserID: authorID,
		Visibility:  VisibilityProject,
	}}
	private := []store.SavedMetricsQuery{{
		ID:          entryID,
		ProjectID:   projectID,
		OwnerUserID: authorID,
		Visibility:  VisibilityPrivate,
	}}

	unshare := validInput()
	unshare.Visibility = VisibilityPrivate
	reader := newService(t, &stubStore{rows: shared}, false)
	if _, err := reader.Update(
		context.Background(), projectID, authorID, entryID, unshare,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("un-sharing without curation error = %v, want ErrDenied", err)
	}

	publish := validInput()
	publish.Visibility = VisibilityProject
	readerOwnPrivate := newService(t, &stubStore{rows: private}, false)
	if _, err := readerOwnPrivate.Update(
		context.Background(), projectID, authorID, entryID, publish,
	); !errors.Is(err, ErrDenied) {
		t.Fatalf("publishing without curation error = %v, want ErrDenied", err)
	}

	curator := newService(t, &stubStore{rows: private}, true)
	if _, err := curator.Update(
		context.Background(), projectID, authorID, entryID, publish,
	); err != nil {
		t.Fatalf("publishing with curation error = %v, want nil", err)
	}
}

// An expression that would be refused at query time is refused at save time,
// where the author is still looking at it.
func TestExpressionsAreValidatedOnSave(t *testing.T) {
	t.Parallel()

	service := newService(t, &stubStore{}, true)
	for _, expression := range []string{
		"",
		"   ",
		"sum by (node",
		`up{job="a"`,
		"not_a_function(up)",
		"up{job=\"" + strings.Repeat("a", 4096) + "\"}",
	} {
		input := validInput()
		input.Expression = expression
		_, err := service.Create(context.Background(), projectID, authorID, input)
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Create(%q) error = %v, want ErrInvalidInput", expression, err)
		}
		var detailed *InputError
		if !errors.As(err, &detailed) || detailed.Detail() == "" {
			t.Errorf("Create(%q) carried no detail for the author", expression)
		}
	}
}

// The expression is stored as written. The Cluster filter belongs to the query
// that runs it, not to the text: baking one in would make a shared entry
// describe the Cluster its author happened to be looking at.
func TestExpressionsAreStoredAsWritten(t *testing.T) {
	t.Parallel()

	savedQueries := &stubStore{}
	service := newService(t, savedQueries, true)
	input := validInput()
	input.Expression = `  up{zke_cluster_id="somebody-else"}  `
	if _, err := service.Create(context.Background(), projectID, authorID, input); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if savedQueries.created.Expression != `up{zke_cluster_id="somebody-else"}` {
		t.Errorf("stored expression = %q", savedQueries.created.Expression)
	}
}

func TestNamesAndDescriptionsAreChecked(t *testing.T) {
	t.Parallel()

	service := newService(t, &stubStore{}, true)
	cases := map[string]Input{
		"empty name":         {Name: "  ", Expression: "up", Visibility: VisibilityPrivate},
		"long name":          {Name: strings.Repeat("a", 101), Expression: "up", Visibility: VisibilityPrivate},
		"newline in name":    {Name: "a\nb", Expression: "up", Visibility: VisibilityPrivate},
		"long description":   {Name: "a", Description: strings.Repeat("b", 501), Expression: "up", Visibility: VisibilityPrivate},
		"unknown visibility": {Name: "a", Expression: "up", Visibility: "world"},
	}
	for name, input := range cases {
		if _, err := service.Create(
			context.Background(), projectID, authorID, input,
		); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("Create(%s) error = %v, want ErrInvalidInput", name, err)
		}
	}
}

// The listing reports what each reader may do with each row, so the Console can
// hide an action it would only be refused for.
func TestListReportsWhatTheReaderMayEdit(t *testing.T) {
	t.Parallel()

	rows := []store.SavedMetricsQuery{
		{ID: entryID, Visibility: VisibilityProject, OwnerUserID: strangerID},
		{ID: "own", Visibility: VisibilityPrivate, OwnerUserID: authorID},
	}
	reader := newService(t, &stubStore{rows: rows}, false)
	items, err := reader.List(context.Background(), projectID, authorID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if items[0].Editable {
		t.Error("a shared entry is editable without curation")
	}
	if !items[1].Editable {
		t.Error("a reader cannot edit their own private entry")
	}

	curator := newService(t, &stubStore{rows: rows}, true)
	curated, err := curator.List(context.Background(), projectID, authorID)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !curated[0].Editable {
		t.Error("a shared entry is not editable with curation")
	}
}

func TestStoreFailuresAreTranslated(t *testing.T) {
	t.Parallel()

	cases := map[error]error{
		store.ErrSavedMetricsQueryConflict: ErrConflict,
		store.ErrSavedMetricsQueryLimit:    ErrLimit,
	}
	for from, want := range cases {
		service := newService(t, &stubStore{err: from}, true)
		if _, err := service.Create(
			context.Background(), projectID, authorID, validInput(),
		); !errors.Is(err, want) {
			t.Errorf("Create() error = %v, want %v", err, want)
		}
	}
	missing := newService(t, &stubStore{}, true)
	if _, err := missing.Update(
		context.Background(), projectID, authorID, entryID, validInput(),
	); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(missing) error = %v, want ErrNotFound", err)
	}
}
