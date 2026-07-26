package httpapi

import (
	"context"
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
	}
	if err := yaml.Unmarshal(contents, &contract); err != nil {
		t.Fatalf("parse OpenAPI contract: %v", err)
	}
	if contract.OpenAPI != "3.1.0" {
		t.Fatalf("OpenAPI version = %q, want 3.1.0", contract.OpenAPI)
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
