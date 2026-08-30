package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/togettoyou/zke/pkg/server/store"
)

const (
	savedQueryTenant   = "00000000-0000-0000-0000-0000000000d1"
	savedQueryProject  = "00000000-0000-0000-0000-0000000000d2"
	savedQueryProject2 = "00000000-0000-0000-0000-0000000000d3"
	savedQueryAuthor   = "00000000-0000-0000-0000-0000000000d4"
	savedQueryOther    = "00000000-0000-0000-0000-0000000000d5"
)

func seedSavedQueryFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	now := time.Now().UTC()
	for _, statement := range []struct {
		sql       string
		arguments []any
	}{
		{`INSERT INTO tenants (id, name, status)
		  VALUES ($1, 'library', 'active')`, []any{savedQueryTenant}},
		{`INSERT INTO projects (id, tenant_id, name, status)
		  VALUES ($1, $2, 'library', 'active')`, []any{savedQueryProject, savedQueryTenant}},
		{`INSERT INTO projects (id, tenant_id, name, status)
		  VALUES ($1, $2, 'library-two', 'active')`, []any{savedQueryProject2, savedQueryTenant}},
		{`INSERT INTO users (id, username_normalized, display_name, password_hash,
		                     status, password_changed_at)
		  VALUES ($1, 'author', 'Author', 'not-used', 'active', $2)`,
			[]any{savedQueryAuthor, now}},
		{`INSERT INTO users (id, username_normalized, display_name, password_hash,
		                     status, password_changed_at)
		  VALUES ($1, 'other', 'Other', 'not-used', 'active', $2)`,
			[]any{savedQueryOther, now}},
	} {
		if _, err := pool.Exec(ctx, statement.sql, statement.arguments...); err != nil {
			t.Fatalf("seed saved query fixture: %v", err)
		}
	}
}

func createSavedQuery(
	t *testing.T,
	ctx context.Context,
	savedQueries *store.MetricsSavedQueryStore,
	id string,
	projectID string,
	ownerUserID string,
	visibility string,
	name string,
) (store.SavedMetricsQuery, error) {
	t.Helper()
	return savedQueries.CreateSavedMetricsQuery(ctx, store.CreateSavedMetricsQueryParams{
		ID:          id,
		ProjectID:   projectID,
		OwnerUserID: ownerUserID,
		Visibility:  visibility,
		Name:        name,
		Expression:  "up",
		Now:         time.Now().UTC().Truncate(time.Microsecond),
	})
}

// The listing is the read boundary. Somebody else's private entry must not be
// reachable through it at all — not returned and then filtered, which is one
// forgotten branch away from handing it over.
func TestSavedMetricsQueriesAreVisibleOnlyWhereTheyShouldBe(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	seedSavedQueryFixture(t, ctx, pool)
	savedQueries := store.NewMetricsSavedQueryStore(pool)

	mine := "00000000-0000-0000-0000-0000000000e1"
	theirs := "00000000-0000-0000-0000-0000000000e2"
	shared := "00000000-0000-0000-0000-0000000000e3"
	elsewhere := "00000000-0000-0000-0000-0000000000e4"
	for _, seed := range []struct {
		id          string
		projectID   string
		ownerUserID string
		visibility  string
		name        string
	}{
		{mine, savedQueryProject, savedQueryAuthor, "private", "mine"},
		{theirs, savedQueryProject, savedQueryOther, "private", "theirs"},
		{shared, savedQueryProject, savedQueryOther, "project", "shared"},
		{elsewhere, savedQueryProject2, savedQueryAuthor, "project", "elsewhere"},
	} {
		if _, err := createSavedQuery(
			t, ctx, savedQueries,
			seed.id, seed.projectID, seed.ownerUserID, seed.visibility, seed.name,
		); err != nil {
			t.Fatalf("create %s: %v", seed.name, err)
		}
	}

	items, err := savedQueries.ListSavedMetricsQueries(ctx, savedQueryProject, savedQueryAuthor)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]store.SavedMetricsQuery, len(items))
	for _, item := range items {
		seen[item.ID] = item
	}
	if _, found := seen[mine]; !found {
		t.Error("the reader's own private entry is missing")
	}
	if _, found := seen[shared]; !found {
		t.Error("a shared entry is missing")
	}
	if _, found := seen[theirs]; found {
		t.Error("somebody else's private entry was returned")
	}
	if _, found := seen[elsewhere]; found {
		t.Error("another Project's entry was returned")
	}
	// The author's name comes back with the row, so a picker can say whose
	// query it is without a second round trip.
	if seen[shared].OwnerDisplayName != "Other" {
		t.Errorf("owner display name = %q", seen[shared].OwnerDisplayName)
	}
	// Ordered for a picker, case-insensitively by name.
	if items[0].Name != "mine" || items[1].Name != "shared" {
		t.Errorf("listing order = %q, %q", items[0].Name, items[1].Name)
	}

	// An identifier from another Project must not be reachable even when it is
	// spelled correctly: the Project is part of the lookup, not a check made
	// afterwards.
	if _, err := savedQueries.GetSavedMetricsQuery(
		ctx, savedQueryProject, elsewhere,
	); !errors.Is(err, store.ErrSavedMetricsQueryNotFound) {
		t.Errorf("cross-Project get error = %v, want ErrSavedMetricsQueryNotFound", err)
	}
}

// Names collide only where they are seen together.
func TestSavedMetricsQueryNamesAreUniqueWhereTheyAreSeen(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	seedSavedQueryFixture(t, ctx, pool)
	savedQueries := store.NewMetricsSavedQueryStore(pool)

	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-0000000000f1",
		savedQueryProject, savedQueryAuthor, "private", "内存用量",
	); err != nil {
		t.Fatal(err)
	}
	// Two people may each keep a private entry with the same name: neither can
	// see the other's, so there is no list for them to collide in.
	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-0000000000f2",
		savedQueryProject, savedQueryOther, "private", "内存用量",
	); err != nil {
		t.Errorf("two private entries with one name collided: %v", err)
	}
	// The same author may not keep two, differing only in case.
	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-0000000000f3",
		savedQueryProject, savedQueryAuthor, "private", "内存用量",
	); !errors.Is(err, store.ErrSavedMetricsQueryConflict) {
		t.Errorf("duplicate private name error = %v, want conflict", err)
	}
	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-0000000000f4",
		savedQueryProject, savedQueryAuthor, "project", "CPU",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-0000000000f5",
		savedQueryProject, savedQueryOther, "project", "cpu",
	); !errors.Is(err, store.ErrSavedMetricsQueryConflict) {
		t.Errorf("duplicate shared name error = %v, want conflict", err)
	}
}

// Sharing outlives its author; a private entry does not.
func TestDeletingAnAuthorKeepsWhatTheyShared(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	seedSavedQueryFixture(t, ctx, pool)
	savedQueries := store.NewMetricsSavedQueryStore(pool)

	private := "00000000-0000-0000-0000-00000000ff01"
	shared := "00000000-0000-0000-0000-00000000ff02"
	if _, err := createSavedQuery(t, ctx, savedQueries,
		private, savedQueryProject, savedQueryAuthor, "private", "private",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := createSavedQuery(t, ctx, savedQueries,
		shared, savedQueryProject, savedQueryAuthor, "project", "shared",
	); err != nil {
		t.Fatal(err)
	}

	// The two statements the user deletion path runs, in its order.
	if _, err := pool.Exec(ctx,
		`DELETE FROM metrics_saved_queries
		 WHERE owner_user_id = $1 AND visibility = 'private'`, savedQueryAuthor,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, savedQueryAuthor); err != nil {
		t.Fatalf("deleting the author was refused: %v", err)
	}

	if _, err := savedQueries.GetSavedMetricsQuery(
		ctx, savedQueryProject, private,
	); !errors.Is(err, store.ErrSavedMetricsQueryNotFound) {
		t.Errorf("the author's private entry survived them: %v", err)
	}
	survivor, err := savedQueries.GetSavedMetricsQuery(ctx, savedQueryProject, shared)
	if err != nil {
		t.Fatalf("the Project lost a shared entry with its author: %v", err)
	}
	if survivor.OwnerUserID != "" || survivor.OwnerDisplayName != "" {
		t.Errorf("a deleted author is still named: %+v", survivor)
	}
}

// Deleting the Project takes its library with it.
func TestDeletingAProjectRemovesItsSavedMetricsQueries(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	seedSavedQueryFixture(t, ctx, pool)
	savedQueries := store.NewMetricsSavedQueryStore(pool)

	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-00000000fe01",
		savedQueryProject2, savedQueryAuthor, "project", "shared",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(
		ctx, `DELETE FROM projects WHERE id = $1`, savedQueryProject2,
	); err != nil {
		t.Fatalf("deleting the Project was refused: %v", err)
	}
	var remaining int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM metrics_saved_queries WHERE project_id = $1`,
		savedQueryProject2,
	).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Errorf("%d saved queries outlived their Project", remaining)
	}
}

// The ceiling holds, and it is enforced by the insert rather than by a count
// somebody read a moment earlier.
func TestSavedMetricsQueryLimitIsEnforcedByTheInsert(t *testing.T) {
	databaseURL := requireAuthTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)
	seedSavedQueryFixture(t, ctx, pool)
	savedQueries := store.NewMetricsSavedQueryStore(pool)

	now := time.Now().UTC().Truncate(time.Microsecond)
	// Seeded directly so the test spends its time on the boundary rather than
	// on two hundred round trips through the ceiling check.
	if _, err := pool.Exec(ctx, `
INSERT INTO metrics_saved_queries (
    id, project_id, owner_user_id, visibility, name, description, expression,
    created_at, updated_at
)
SELECT gen_random_uuid(), $1, $2, 'private', 'seed-' || generated, '', 'up', $3, $3
FROM generate_series(1, $4) AS generated`,
		savedQueryProject,
		savedQueryAuthor,
		now,
		store.MaxSavedMetricsQueriesPerProject,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-00000000fd01",
		savedQueryProject, savedQueryAuthor, "private", "one too many",
	); !errors.Is(err, store.ErrSavedMetricsQueryLimit) {
		t.Errorf("create past the ceiling error = %v, want ErrSavedMetricsQueryLimit", err)
	}
	// Another Project is unaffected: the ceiling is per Project.
	if _, err := createSavedQuery(t, ctx, savedQueries,
		"00000000-0000-0000-0000-00000000fd02",
		savedQueryProject2, savedQueryAuthor, "private", "elsewhere",
	); err != nil {
		t.Errorf("a full Project blocked a different one: %v", err)
	}
}
