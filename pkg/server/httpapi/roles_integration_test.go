package httpapi

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/togettoyou/zke/pkg/server/accessmanagement"
	"github.com/togettoyou/zke/pkg/server/audit"
	"github.com/togettoyou/zke/pkg/server/auth"
	"github.com/togettoyou/zke/pkg/server/rbac"
	"github.com/togettoyou/zke/pkg/server/store"
)

/*
 * The role lifecycle over HTTP, and the wall around it.
 *
 * The interesting case is not that an administrator can create a role — it is
 * what happens when the holder of that role tries to use `rbac.manage` to reach
 * past their own permissions. That is the whole reason roles can be data at all,
 * so it is exercised end to end rather than only at the service boundary.
 */
func TestRoleHTTPFlowEnforcesThePermissionCeiling(t *testing.T) {
	databaseURL := requireHTTPTestDatabaseURL(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	pool := openHTTPTestDatabase(t, ctx, databaseURL)
	applyMigrations(t, ctx, pool)

	adminPassword := []byte("a sufficiently long role administrator passphrase")
	admin, err := auth.CreateInitialAdmin(
		ctx,
		store.NewAuthStore(pool),
		auth.InitialAdminInput{
			Username:    "role-admin",
			DisplayName: "Role Administrator",
			Password:    adminPassword,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	authService := auth.NewService(store.NewAuthStore(pool), auth.ServiceConfig{
		SessionIdleTimeout:          30 * time.Minute,
		SessionAbsoluteTimeout:      8 * time.Hour,
		MaxConcurrentPasswordChecks: 1,
		MaxFailedLoginAttempts:      3,
		AccountLockDuration:         time.Hour,
	})
	adminLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  admin.Username,
		Password:  adminPassword,
		RequestID: "request-role-admin-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	rbacService := rbac.NewService(store.NewRBACStore(pool))
	router := New(
		discardLogger(),
		Dependencies{
			ReadinessCheck: pool.Ping,
			AuthService:    authService,
			AuditService:   audit.NewService(store.NewAuditStore(pool), rbacService),
			RBACService:    rbacService,
			AccessManagementService: accessmanagement.NewService(
				store.NewAccessManagementStore(pool),
				accessmanagement.Config{MaxConcurrentPasswordHashes: 1},
			).WithPermissionAuthority(rbacService),
		},
		Config{Authentication: defaultAuthenticationTestConfig()},
	)

	// The builtin roles are listed, and the permission dictionary reports the
	// administrator's own ceiling as complete.
	roles := accessAPIRequest(router, http.MethodGet, "/api/v1/roles", "", adminLogin)
	if roles.Code != http.StatusOK {
		t.Fatalf("list roles status = %d: %s", roles.Code, roles.Body)
	}
	var rolePage struct {
		Roles []roleResponse `json:"roles"`
	}
	if err := decodeSuccessResponse(roles, &rolePage); err != nil {
		t.Fatal(err)
	}
	if len(rolePage.Roles) != 2 {
		t.Fatalf("builtin role count = %d, want 2", len(rolePage.Roles))
	}
	for _, role := range rolePage.Roles {
		if !role.Builtin {
			t.Fatalf("role %q is not marked builtin", role.Name)
		}
	}

	permissions := accessAPIRequest(
		router, http.MethodGet, "/api/v1/permissions", "", adminLogin,
	)
	if permissions.Code != http.StatusOK {
		t.Fatalf("list permissions status = %d: %s", permissions.Code, permissions.Body)
	}
	var dictionary struct {
		Permissions []struct {
			Name string `json:"name"`
			Held bool   `json:"held"`
		} `json:"permissions"`
	}
	if err := decodeSuccessResponse(permissions, &dictionary); err != nil {
		t.Fatal(err)
	}
	if len(dictionary.Permissions) != len(rbac.Permissions()) {
		t.Fatalf(
			"permission dictionary has %d entries, want %d",
			len(dictionary.Permissions),
			len(rbac.Permissions()),
		)
	}
	for _, permission := range dictionary.Permissions {
		if !permission.Held {
			t.Fatalf("global administrator does not hold %q", permission.Name)
		}
	}

	// A builtin role is not editable, whoever asks.
	var adminRoleID string
	for _, role := range rolePage.Roles {
		if role.Name == "admin" {
			adminRoleID = role.ID
		}
	}
	builtinEdit := accessAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/roles/"+adminRoleID,
		`{"display_name":"改名","permissions":["cluster.read"],"confirm":true}`,
		adminLogin,
	)
	if builtinEdit.Code != http.StatusConflict {
		t.Fatalf("edit builtin role status = %d: %s", builtinEdit.Code, builtinEdit.Body)
	}

	// A narrow role, created by the administrator and bound to a second account.
	created := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/roles",
		`{"name":"delegate","display_name":"授权代理",`+
			`"permissions":["rbac.read","rbac.manage"],"confirm":true}`,
		adminLogin,
	)
	if created.Code != http.StatusCreated {
		t.Fatalf("create role status = %d: %s", created.Code, created.Body)
	}
	var delegateRole roleResponse
	if err := decodeSuccessResponse(created, &delegateRole); err != nil {
		t.Fatal(err)
	}

	delegatePassword := "a sufficiently long delegate account passphrase"
	createdUser := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/users",
		`{"username":"delegate-user","display_name":"Delegate",`+
			`"password":"`+delegatePassword+`"}`,
		adminLogin,
	)
	if createdUser.Code != http.StatusCreated {
		t.Fatalf("create user status = %d: %s", createdUser.Code, createdUser.Body)
	}
	var delegate managedUserResponse
	if err := decodeSuccessResponse(createdUser, &delegate); err != nil {
		t.Fatal(err)
	}
	bound := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/role-bindings",
		`{"subject_id":"`+delegate.ID+`","role":"delegate",`+
			`"scope_type":"global","confirm":true}`,
		adminLogin,
	)
	if bound.Code != http.StatusCreated {
		t.Fatalf("bind delegate status = %d: %s", bound.Code, bound.Body)
	}

	delegateLogin, err := authService.Login(ctx, auth.LoginInput{
		Username:  "delegate-user",
		Password:  []byte(delegatePassword),
		RequestID: "request-delegate-login",
		Now:       time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}

	/*
	 * The delegate holds `rbac.manage` and nothing else worth having. Three ways
	 * to turn that into more, all refused:
	 *   1. write a role carrying a permission they do not hold;
	 *   2. bind the builtin `admin`, which already carries everything;
	 *   3. widen the role they already hold.
	 */
	escalatingRole := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/roles",
		`{"name":"self-promotion","display_name":"提权",`+
			`"permissions":["cluster.secret.read"],"confirm":true}`,
		delegateLogin,
	)
	if escalatingRole.Code != http.StatusForbidden {
		t.Fatalf(
			"delegate created a role beyond its ceiling: status = %d: %s",
			escalatingRole.Code,
			escalatingRole.Body,
		)
	}

	escalatingBinding := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/role-bindings",
		`{"subject_id":"`+delegate.ID+`","role":"admin",`+
			`"scope_type":"global","confirm":true}`,
		delegateLogin,
	)
	if escalatingBinding.Code != http.StatusForbidden {
		t.Fatalf(
			"delegate bound the admin role to itself: status = %d: %s",
			escalatingBinding.Code,
			escalatingBinding.Body,
		)
	}

	escalatingEdit := accessAPIRequest(
		router,
		http.MethodPut,
		"/api/v1/roles/"+delegateRole.ID,
		`{"display_name":"授权代理",`+
			`"permissions":["rbac.read","rbac.manage","cluster.secret.read"],"confirm":true}`,
		delegateLogin,
	)
	if escalatingEdit.Code != http.StatusForbidden {
		t.Fatalf(
			"delegate widened its own role: status = %d: %s",
			escalatingEdit.Code,
			escalatingEdit.Body,
		)
	}

	// What the delegate can do: hand out what it holds.
	permittedRole := accessAPIRequest(
		router,
		http.MethodPost,
		"/api/v1/roles",
		`{"name":"rbac-reader","display_name":"授权查看",`+
			`"permissions":["rbac.read"],"confirm":true}`,
		delegateLogin,
	)
	if permittedRole.Code != http.StatusCreated {
		t.Fatalf(
			"delegate could not create a role within its ceiling: status = %d: %s",
			permittedRole.Code,
			permittedRole.Body,
		)
	}

	// A bound role cannot be removed while somebody holds it.
	boundDelete := accessAPIRequest(
		router,
		http.MethodDelete,
		"/api/v1/roles/"+delegateRole.ID,
		`{"confirm":true}`,
		adminLogin,
	)
	if boundDelete.Code != http.StatusConflict {
		t.Fatalf("delete bound role status = %d: %s", boundDelete.Code, boundDelete.Body)
	}
}
