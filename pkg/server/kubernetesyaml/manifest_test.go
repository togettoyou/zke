package kubernetesyaml

import (
	"errors"
	"strings"
	"testing"
)

func TestDecodeDocumentsSplitsAndKeepsOrder(t *testing.T) {
	t.Parallel()

	manifest := strings.Join([]string{
		"apiVersion: v1",
		"kind: ConfigMap",
		"metadata:",
		"  name: first",
		"---",
		"apiVersion: apps/v1",
		"kind: Deployment",
		"metadata:",
		"  name: second",
		"spec:",
		"  replicas: 3",
		"",
	}, "\n")

	documents, err := DecodeDocuments([]byte(manifest), 10)
	if err != nil {
		t.Fatalf("DecodeDocuments() = %v", err)
	}
	if len(documents) != 2 {
		t.Fatalf("decoded %d documents, want 2", len(documents))
	}
	if documents[0]["kind"] != "ConfigMap" || documents[1]["kind"] != "Deployment" {
		t.Fatalf("documents out of order: %v", documents)
	}
	// The conversion has to keep an integer an integer: a replica count that
	// arrived as a float would be re-encoded as one and rejected by Kubernetes.
	spec, ok := documents[1]["spec"].(map[string]any)
	if !ok {
		t.Fatalf("spec is %T", documents[1]["spec"])
	}
	if replicas, ok := spec["replicas"].(float64); !ok || replicas != 3 {
		t.Fatalf("replicas decoded as %T %v", spec["replicas"], spec["replicas"])
	}
}

// `---` at the head or foot of a file, and the blank document a template that
// rendered nothing leaves behind, are in every real manifest. Refusing them
// would refuse files `kubectl` accepts, for no gain.
func TestDecodeDocumentsSkipsEmptyDocuments(t *testing.T) {
	t.Parallel()

	manifest := "---\n---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: only\n---\n---\n"
	documents, err := DecodeDocuments([]byte(manifest), 10)
	if err != nil {
		t.Fatalf("DecodeDocuments() = %v", err)
	}
	if len(documents) != 1 || documents[0]["kind"] != "ConfigMap" {
		t.Fatalf("decoded %v, want the single ConfigMap", documents)
	}
}

// Every document is held to the rules one document is held to. A manifest is
// exactly where a document meaning something other than what it reads as would
// do the most damage, because nobody reviews thirty objects as closely as one.
func TestDecodeDocumentsRefusesUnsafeDocuments(t *testing.T) {
	t.Parallel()

	const valid = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: first\n"
	testCases := []struct {
		name     string
		manifest string
	}{
		{
			name:     "anchor and alias in a later document",
			manifest: valid + "---\nbase: &shared\n  a: 1\nkind: <<\n",
		},
		{
			name:     "duplicate key in a later document",
			manifest: valid + "---\nkind: ConfigMap\nkind: Secret\n",
		},
		{
			name:     "sequence at the top of a document",
			manifest: valid + "---\n- kind: ConfigMap\n",
		},
		{
			name:     "scalar at the top of a document",
			manifest: valid + "---\njust a string\n",
		},
		{
			name:     "YAML-only tag",
			manifest: valid + "---\nkind: !!binary aGk=\n",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeDocuments([]byte(testCase.manifest), 10); err == nil {
				t.Fatal("DecodeDocuments() accepted an unsafe document")
			} else if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("DecodeDocuments() = %v, want ErrInvalidManifest", err)
			}
		})
	}
}

// The index has to name the document an operator can find, or an error against a
// file of thirty objects is unusable.
func TestDecodeDocumentsReportsTheOffendingDocument(t *testing.T) {
	t.Parallel()

	manifest := "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: first\n" +
		"---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: second\n" +
		"---\nkind: ConfigMap\nkind: Secret\n"
	_, err := DecodeDocuments([]byte(manifest), 10)
	var documentError DocumentError
	if !errors.As(err, &documentError) {
		t.Fatalf("DecodeDocuments() = %v, want a DocumentError", err)
	}
	if documentError.Index != 2 {
		t.Fatalf("DocumentError.Index = %d, want 2", documentError.Index)
	}
	if !strings.Contains(documentError.Error(), "document 3") {
		t.Fatalf("DocumentError.Error() = %q, want it to name document 3", documentError.Error())
	}
}

func TestDecodeDocumentsEnforcesTheLimit(t *testing.T) {
	t.Parallel()

	documents := make([]string, 0, 4)
	for range 4 {
		documents = append(documents, "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: a\n")
	}
	manifest := strings.Join(documents, "---\n")
	if _, err := DecodeDocuments([]byte(manifest), 3); !errors.Is(err, ErrTooManyDocuments) {
		t.Fatalf("DecodeDocuments() = %v, want ErrTooManyDocuments", err)
	}
	if _, err := DecodeDocuments([]byte(manifest), 4); err != nil {
		t.Fatalf("DecodeDocuments() at the limit = %v", err)
	}
}

func TestDecodeDocumentsRefusesAManifestWithNoDocuments(t *testing.T) {
	t.Parallel()

	for _, manifest := range []string{"", "   \n", "---\n---\n", "# only a comment\n"} {
		if _, err := DecodeDocuments([]byte(manifest), 10); !errors.Is(err, ErrEmptyManifest) {
			t.Fatalf("DecodeDocuments(%q) = %v, want ErrEmptyManifest", manifest, err)
		}
	}
}
