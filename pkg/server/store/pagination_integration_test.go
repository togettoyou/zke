package store_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/store"
	"github.com/togettoyou/zke/pkg/shared/pagination"
)

// These exercise the paged list queries against a real PostgreSQL, because the
// filter, search and offset clauses are raw SQL with positional parameters that
// no amount of Go-level testing can validate.
func TestPagedListQueries(t *testing.T) {
	databaseURL := os.Getenv("ZKE_TEST_DATABASE_URL")
	if databaseURL == "" {
		if os.Getenv("CI") != "" {
			t.Fatal("ZKE_TEST_DATABASE_URL is required in CI")
		}
		t.Skip("ZKE_TEST_DATABASE_URL is not configured")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	pool := openIsolatedDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	const (
		tenantAlphaID = "00000000-0000-4000-8000-0000000000a1"
		tenantBetaID  = "00000000-0000-4000-8000-0000000000a2"
		projectOneID  = "00000000-0000-4000-8000-0000000000b1"
		projectTwoID  = "00000000-0000-4000-8000-0000000000b2"
	)
	now := time.Now().UTC().Truncate(time.Microsecond)

	if _, err := pool.Exec(ctx, `
INSERT INTO tenants (id, name, status) VALUES
    ($1, 'Alpha Tenant', 'active'),
    ($2, 'Beta Tenant', 'suspended')
`, tenantAlphaID, tenantBetaID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO projects (id, tenant_id, name, status) VALUES
    ($1, $3, 'Apollo Project', 'active'),
    ($2, $3, 'Zephyr Project', 'suspended')
`, projectOneID, projectTwoID, tenantAlphaID); err != nil {
		t.Fatal(err)
	}

	// Enough users to page through, with a searchable name on one of them.
	for index := range 5 {
		if _, err := pool.Exec(ctx, `
INSERT INTO users (
    id, username_normalized, display_name, password_hash, status,
    password_changed_at
) VALUES (gen_random_uuid(), $1, $2, 'hash', $3, now())
`,
			fmt.Sprintf("operator%02d", index),
			fmt.Sprintf("Operator %02d", index),
			map[bool]string{true: "active", false: "disabled"}[index%2 == 0],
		); err != nil {
			t.Fatal(err)
		}
	}

	accessStore := store.NewAccessManagementStore(pool)

	t.Run("users page with a stable total", func(t *testing.T) {
		first, total, err := accessStore.ListUsers(ctx, store.ListManagedUsersParams{
			Page: pagination.Request{Limit: 2, Offset: 0},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 5 || len(first) != 2 {
			t.Fatalf("first page = %d rows, total %d, want 2 rows and total 5", len(first), total)
		}
		second, total, err := accessStore.ListUsers(ctx, store.ListManagedUsersParams{
			Page: pagination.Request{Limit: 2, Offset: 2},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 5 || len(second) != 2 {
			t.Fatalf("second page = %d rows, total %d, want 2 rows and total 5", len(second), total)
		}
		if first[0].ID == second[0].ID {
			t.Fatal("offset did not advance the page")
		}
		// Paging past the end must keep reporting the filtered total.
		beyond, total, err := accessStore.ListUsers(ctx, store.ListManagedUsersParams{
			Page: pagination.Request{Limit: 2, Offset: 500},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(beyond) != 0 || total != 5 {
			t.Fatalf("beyond-end page = %d rows, total %d, want 0 rows and total 5", len(beyond), total)
		}
	})

	t.Run("users filter by status and search", func(t *testing.T) {
		active, total, err := accessStore.ListUsers(ctx, store.ListManagedUsersParams{
			Status: "active",
			Page:   pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 3 || len(active) != 3 {
			t.Fatalf("active users = %d rows, total %d, want 3", len(active), total)
		}
		for _, item := range active {
			if item.Status != "active" {
				t.Fatalf("status filter leaked %q", item.Status)
			}
		}
		found, total, err := accessStore.ListUsers(ctx, store.ListManagedUsersParams{
			Search: "operator03",
			Page:   pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(found) != 1 || found[0].Username != "operator03" {
			t.Fatalf("search returned %+v, total %d", found, total)
		}
		// A search term containing LIKE wildcards must be matched literally.
		none, total, err := accessStore.ListUsers(ctx, store.ListManagedUsersParams{
			Search: "%oper%",
			Page:   pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(none) != 0 {
			t.Fatalf("wildcard search was interpreted as a pattern: %d rows", len(none))
		}
	})

	resourceStore := store.NewResourceManagementStore(pool)

	t.Run("tenants respect pushed-down visibility", func(t *testing.T) {
		all, total, err := resourceStore.ListTenants(ctx, store.ListTenantsParams{
			Visibility: store.ScopeVisibility{Global: true},
			Page:       pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 2 || len(all) != 2 {
			t.Fatalf("global visibility = %d tenants, total %d, want 2", len(all), total)
		}

		scoped, total, err := resourceStore.ListTenants(ctx, store.ListTenantsParams{
			Visibility: store.ScopeVisibility{TenantIDs: []string{tenantBetaID}},
			Page:       pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(scoped) != 1 || scoped[0].ID != tenantBetaID {
			t.Fatalf("tenant-scoped visibility returned %+v, total %d", scoped, total)
		}

		// A project-scoped binding must still surface the owning tenant.
		viaProject, total, err := resourceStore.ListTenants(ctx, store.ListTenantsParams{
			Visibility: store.ScopeVisibility{ProjectTenantIDs: []string{tenantAlphaID}},
			Page:       pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(viaProject) != 1 || viaProject[0].ID != tenantAlphaID {
			t.Fatalf("project-derived visibility returned %+v, total %d", viaProject, total)
		}

		// No visibility at all must return nothing rather than everything.
		empty, total, err := resourceStore.ListTenants(ctx, store.ListTenantsParams{
			Page: pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 0 || len(empty) != 0 {
			t.Fatalf("empty visibility returned %d tenants, total %d", len(empty), total)
		}
	})

	t.Run("tenants filter by status and search", func(t *testing.T) {
		suspended, total, err := resourceStore.ListTenants(ctx, store.ListTenantsParams{
			Visibility: store.ScopeVisibility{Global: true},
			Status:     "suspended",
			Page:       pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(suspended) != 1 || suspended[0].ID != tenantBetaID {
			t.Fatalf("status filter returned %+v, total %d", suspended, total)
		}
		// Search must be case-insensitive against the lowercased term.
		matched, total, err := resourceStore.ListTenants(ctx, store.ListTenantsParams{
			Visibility: store.ScopeVisibility{Global: true},
			Search:     "alpha",
			Page:       pagination.Request{Limit: 50},
		})
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(matched) != 1 || matched[0].ID != tenantAlphaID {
			t.Fatalf("search returned %+v, total %d", matched, total)
		}
	})

	t.Run("projects respect visibility inside a tenant", func(t *testing.T) {
		scoped, total, err := resourceStore.ListTenantProjects(
			ctx,
			store.ListTenantProjectsParams{
				TenantID:   tenantAlphaID,
				Visibility: store.ScopeVisibility{ProjectIDs: []string{projectTwoID}},
				Page:       pagination.Request{Limit: 50},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(scoped) != 1 || scoped[0].ID != projectTwoID {
			t.Fatalf("project visibility returned %+v, total %d", scoped, total)
		}
		all, total, err := resourceStore.ListTenantProjects(
			ctx,
			store.ListTenantProjectsParams{
				TenantID:   tenantAlphaID,
				Visibility: store.ScopeVisibility{Global: true},
				Status:     "active",
				Page:       pagination.Request{Limit: 50},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(all) != 1 || all[0].ID != projectOneID {
			t.Fatalf("active project filter returned %+v, total %d", all, total)
		}
	})

	t.Run("enrollment lifecycle status is filtered in SQL", func(t *testing.T) {
		enrollmentStore := store.NewEnrollmentStore(pool)
		if _, err := pool.Exec(ctx, `
INSERT INTO enrollments (
    id, tenant_id, project_id, cluster_name, created_by_user_id,
    token_digest, expires_at, idempotency_key,
    endpoint_profile_id, endpoint_profile_revision, registration_url,
    quic_address, registration_ca_certificate_pem,
    agent_image, agent_namespace, agent_image_pull_policy
)
SELECT
    gen_random_uuid(), $1, $2, value.name, (SELECT id FROM users LIMIT 1),
    decode(value.digest, 'hex'), value.expires_at,
    'idempotency-' || value.name,
    '00000000-0000-0000-0000-000000000010', 1, 'http://127.0.0.1:8080',
    '127.0.0.1:8443', '', 'zke-agent:test', 'zke-system', 'IfNotPresent'
FROM (VALUES
    ('active-cluster', repeat('a1', 32), $3::timestamptz),
    ('expired-cluster', repeat('b2', 32), $4::timestamptz)
) AS value(name, digest, expires_at)
`, tenantAlphaID, projectOneID, now.Add(time.Hour), now.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}

		for _, testCase := range []struct {
			status string
			name   string
		}{
			{"active", "active-cluster"},
			{"expired", "expired-cluster"},
		} {
			items, total, err := enrollmentStore.ListEnrollments(
				ctx,
				store.ListEnrollmentsParams{
					ProjectID: projectOneID,
					Status:    testCase.status,
					Now:       now,
					Page:      pagination.Request{Limit: 50},
				},
			)
			if err != nil {
				t.Fatal(err)
			}
			if total != 1 || len(items) != 1 || items[0].ClusterName != testCase.name {
				t.Fatalf(
					"status %q returned %+v, total %d",
					testCase.status,
					items,
					total,
				)
			}
		}

		searched, total, err := enrollmentStore.ListEnrollments(
			ctx,
			store.ListEnrollmentsParams{
				ProjectID: projectOneID,
				Search:    "expired",
				Now:       now,
				Page:      pagination.Request{Limit: 50},
			},
		)
		if err != nil {
			t.Fatal(err)
		}
		if total != 1 || len(searched) != 1 {
			t.Fatalf("enrollment search returned %+v, total %d", searched, total)
		}
	})
}
