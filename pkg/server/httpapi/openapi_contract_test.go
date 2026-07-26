package httpapi

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"
)

var ginPathParameter = regexp.MustCompile(`:([A-Za-z0-9_]+)`)

func TestOpenAPIContractCoversRegisteredRoutes(t *testing.T) {
	t.Parallel()

	contractPath := filepath.Join(
		"..", "..", "..", "api", "openapi", "zke-server.v1.yaml",
	)
	contents, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		OpenAPI string                            `yaml:"openapi"`
		Paths   map[string]map[string]interface{} `yaml:"paths"`
		Servers []struct {
			URL string `yaml:"url"`
		} `yaml:"servers"`
	}
	if err := yaml.Unmarshal(contents, &contract); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	if contract.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", contract.OpenAPI)
	}
	if len(contract.Servers) != 1 || contract.Servers[0].URL != "/" {
		t.Fatalf("OpenAPI server must use the same-origin relative URL")
	}
	var document map[string]interface{}
	if err := yaml.Unmarshal(contents, &document); err != nil {
		t.Fatalf("parse OpenAPI document tree: %v", err)
	}
	validateOpenAPIReferences(t, document, document)
	engine, ok := testRouter(func(_ context.Context) error { return nil }).(*gin.Engine)
	if !ok {
		t.Fatal("test router is not a Gin engine")
	}
	registered := make(map[string]struct{})
	for _, route := range engine.Routes() {
		path := ginPathParameter.ReplaceAllString(route.Path, `{$1}`)
		registered[strings.ToUpper(route.Method)+" "+path] = struct{}{}
	}
	documented := make(map[string]struct{})
	operationIDs := make(map[string]string)
	for path, item := range contract.Paths {
		for method, rawOperation := range item {
			upperMethod := strings.ToUpper(method)
			switch upperMethod {
			case "GET", "POST", "PUT", "PATCH", "DELETE":
			default:
				continue
			}
			documented[upperMethod+" "+path] = struct{}{}
			operation, ok := rawOperation.(map[string]interface{})
			if !ok {
				t.Fatalf("%s %s operation is not an object", upperMethod, path)
			}
			operationID, _ := operation["operationId"].(string)
			if operationID == "" {
				t.Errorf("%s %s has no operationId", upperMethod, path)
				continue
			}
			if previous, exists := operationIDs[operationID]; exists {
				t.Errorf(
					"operationId %q is shared by %s and %s %s",
					operationID,
					previous,
					upperMethod,
					path,
				)
			}
			operationIDs[operationID] = upperMethod + " " + path
			validateOperationResponses(t, upperMethod, path, operation)
		}
	}
	for route := range registered {
		if _, exists := documented[route]; !exists {
			t.Errorf("registered route %q is missing from OpenAPI", route)
		}
	}
	for route := range documented {
		if _, exists := registered[route]; !exists {
			t.Errorf("OpenAPI operation %q has no registered route", route)
		}
	}
	validatePhaseOneFrontendContract(t, contract.Paths)
}

func validateOperationResponses(
	t *testing.T,
	method string,
	path string,
	operation map[string]interface{},
) {
	t.Helper()
	responses, ok := operation["responses"].(map[string]interface{})
	if !ok {
		t.Errorf("%s %s has no responses", method, path)
		return
	}
	hasSuccess := false
	hasClientError := false
	for status := range responses {
		hasSuccess = hasSuccess || strings.HasPrefix(status, "2")
		hasClientError = hasClientError || strings.HasPrefix(status, "4")
	}
	if !hasSuccess {
		t.Errorf("%s %s has no success response", method, path)
	}
	if path != "/healthz" && path != "/readyz" && !hasClientError {
		t.Errorf("%s %s has no documented client error", method, path)
	}
	if operationUsesSecurityScheme(operation, "sessionCookie") {
		if _, exists := responses["401"]; !exists {
			t.Errorf("%s %s has no documented unauthenticated response", method, path)
		}
		if method != http.MethodGet {
			if _, exists := responses["403"]; !exists {
				t.Errorf("%s %s has no documented CSRF or forbidden response", method, path)
			}
		}
	}
}

func validatePhaseOneFrontendContract(
	t *testing.T,
	paths map[string]map[string]interface{},
) {
	t.Helper()
	assertSchemaReference(
		t,
		operationAt(t, paths, "/api/v1/auth/me", "get"),
		[]string{"responses", "200", "content", "application/json", "schema"},
		"#/components/schemas/CurrentSession",
	)
	assertSchemaReference(
		t,
		operationAt(t, paths, "/api/v1/auth/password", "post"),
		[]string{"requestBody", "content", "application/json", "schema"},
		"#/components/schemas/ChangePasswordRequest",
	)
	agentEnrollment := operationAt(t, paths, "/agent-api/v1/enroll", "post")
	assertSchemaReference(
		t,
		agentEnrollment,
		[]string{"requestBody", "content", "application/json", "schema"},
		"#/components/schemas/AgentEnrollmentRequest",
	)
	for _, status := range []string{"200", "201"} {
		assertSchemaReference(
			t,
			agentEnrollment,
			[]string{"responses", status, "content", "application/json", "schema"},
			"#/components/schemas/AgentEnrollmentResponse",
		)
	}

	listOperations := []struct {
		path   string
		method string
	}{
		{"/api/v1/users", "get"},
		{"/api/v1/role-bindings", "get"},
		{"/api/v1/tenants", "get"},
		{"/api/v1/tenants/{tenant_id}/projects", "get"},
		{"/api/v1/projects/{project_id}/clusters", "get"},
		{"/api/v1/projects/{project_id}/cluster-enrollments", "get"},
	}
	for _, listOperation := range listOperations {
		operation := operationAt(
			t, paths, listOperation.path, listOperation.method,
		)
		for _, reference := range []string{
			"#/components/parameters/ListLimit",
			"#/components/parameters/ListOffset",
			"#/components/parameters/ListSearch",
		} {
			if !operationHasParameterReference(operation, reference) {
				t.Errorf(
					"%s %s is missing parameter %s",
					strings.ToUpper(listOperation.method),
					listOperation.path,
					reference,
				)
			}
		}
		assertSchemaReference(
			t,
			operation,
			[]string{
				"responses", "200", "content", "application/json",
				"schema", "properties", "pagination",
			},
			"#/components/schemas/Pagination",
		)
	}
}

func operationAt(
	t *testing.T,
	paths map[string]map[string]interface{},
	path string,
	method string,
) map[string]interface{} {
	t.Helper()
	item, exists := paths[path]
	if !exists {
		t.Fatalf("OpenAPI path %s does not exist", path)
	}
	operation, ok := item[method].(map[string]interface{})
	if !ok {
		t.Fatalf("OpenAPI operation %s %s does not exist", strings.ToUpper(method), path)
	}
	return operation
}

func operationUsesSecurityScheme(
	operation map[string]interface{},
	scheme string,
) bool {
	requirements, _ := operation["security"].([]interface{})
	for _, rawRequirement := range requirements {
		requirement, _ := rawRequirement.(map[string]interface{})
		if _, exists := requirement[scheme]; exists {
			return true
		}
	}
	return false
}

func operationHasParameterReference(
	operation map[string]interface{},
	reference string,
) bool {
	parameters, _ := operation["parameters"].([]interface{})
	for _, rawParameter := range parameters {
		parameter, _ := rawParameter.(map[string]interface{})
		if parameter["$ref"] == reference {
			return true
		}
	}
	return false
}

func assertSchemaReference(
	t *testing.T,
	operation map[string]interface{},
	path []string,
	want string,
) {
	t.Helper()
	var current interface{} = operation
	for _, part := range path {
		object, ok := current.(map[string]interface{})
		if !ok {
			t.Errorf("OpenAPI value at %s is not an object", strings.Join(path, "."))
			return
		}
		current, ok = object[part]
		if !ok {
			t.Errorf("OpenAPI value at %s does not exist", strings.Join(path, "."))
			return
		}
	}
	schema, ok := current.(map[string]interface{})
	if !ok || schema["$ref"] != want {
		t.Errorf("OpenAPI schema reference at %s = %#v, want %s",
			strings.Join(path, "."),
			current,
			want,
		)
	}
}

func validateOpenAPIReferences(
	t *testing.T,
	root map[string]interface{},
	value interface{},
) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]interface{}:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") {
					t.Errorf("unsupported OpenAPI reference %#v", child)
					continue
				}
				if !openAPIReferenceExists(root, reference) {
					t.Errorf("OpenAPI reference %q does not exist", reference)
				}
				continue
			}
			validateOpenAPIReferences(t, root, child)
		}
	case []interface{}:
		for _, child := range typed {
			validateOpenAPIReferences(t, root, child)
		}
	}
}

func openAPIReferenceExists(
	root map[string]interface{},
	reference string,
) bool {
	var current interface{} = root
	for _, part := range strings.Split(strings.TrimPrefix(reference, "#/"), "/") {
		object, ok := current.(map[string]interface{})
		if !ok {
			return false
		}
		current, ok = object[part]
		if !ok {
			return false
		}
	}
	return true
}
