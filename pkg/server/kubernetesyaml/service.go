package kubernetesyaml

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"gopkg.in/yaml.v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigyaml "sigs.k8s.io/yaml"
)

var (
	ErrInvalidManifest        = errors.New("invalid Kubernetes YAML manifest")
	ErrResourceUIDChanged     = errors.New("Kubernetes resource UID changed")
	ErrResourceVersionChanged = errors.New("Kubernetes resource version changed")
)

type ResourceService interface {
	GetResource(
		context.Context,
		kubernetesresource.GetResourceInput,
	) (map[string]any, error)
	UpdateResource(
		context.Context,
		kubernetesresource.UpdateResourceInput,
	) (map[string]any, error)
}

/*
 * The rules of a resource family that has an API of its own.
 *
 * A YAML document can say anything a typed form can, so a family whose typed
 * API refuses to write certain things has to refuse them here too — otherwise
 * the editor is a way around its own guard rails rather than a way to reach the
 * fields the form does not model. It is given both the object as it exists and
 * the object as submitted, because some of these rules are about the change
 * rather than about either state: a Secret may keep the type it has and may not
 * be given another one.
 *
 * Called after the identity check and before anything is sent, so a refusal
 * costs no write and no round trip to the Cluster.
 *
 * The third argument is what the caller may hand out about Secrets, resolved per
 * request rather than at construction: a Kubernetes PolicyRule is a way to give
 * somebody else access, and whether this manifest may do that depends on who is
 * submitting it. Guards that grant nothing ignore it.
 */
type ManifestGuard func(
	current map[string]any,
	submitted map[string]any,
	grant kubernetesresource.SecretRuleGrant,
) error

type Service struct {
	resources ResourceService
	guard     ManifestGuard
}

type GetInput struct {
	ClusterID string
	Resource  kubernetesresource.ResourceIdentity
	Namespace string
	Name      string
}

type UpdateInput struct {
	GetInput
	Manifest []byte
	// Passed to the guard. Zero-valued grants nothing, so a caller that does not
	// set it gets the same treatment as one holding no Secret permissions.
	SecretGrant    kubernetesresource.SecretRuleGrant
	DryRun         bool
	Confirm        bool
	FieldManager   string
	IdempotencyKey string
}

type Result struct {
	Manifest        []byte
	UID             string
	ResourceVersion string
}

// NewService builds the YAML API over one resource accessor. A nil guard leaves
// the manifest to Kubernetes' own validation, which is what the generic
// resource families get.
func NewService(resources ResourceService, guard ManifestGuard) *Service {
	return &Service{resources: resources, guard: guard}
}

func (service *Service) Get(
	ctx context.Context,
	input GetInput,
) (Result, error) {
	object, err := service.resources.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: input.ClusterID,
			Resource:  input.Resource,
			Namespace: input.Namespace,
			Name:      input.Name,
		},
	)
	if err != nil {
		return Result{}, err
	}
	return resultFromObject(object)
}

func (service *Service) Update(
	ctx context.Context,
	input UpdateInput,
) (Result, error) {
	object, err := decodeManifest(input.Manifest)
	if err != nil {
		return Result{}, err
	}

	current, err := service.resources.GetResource(
		ctx,
		kubernetesresource.GetResourceInput{
			ClusterID: input.ClusterID,
			Resource:  input.Resource,
			Namespace: input.Namespace,
			Name:      input.Name,
		},
	)
	if err != nil {
		return Result{}, err
	}
	if err := validateIdentity(input.GetInput, object, current); err != nil {
		return Result{}, err
	}
	if service.guard != nil {
		if err := service.guard(current, object, input.SecretGrant); err != nil {
			return Result{}, err
		}
	}

	updated, err := service.resources.UpdateResource(
		ctx,
		kubernetesresource.UpdateResourceInput{
			ClusterID: input.ClusterID,
			Resource:  input.Resource,
			Namespace: input.Namespace,
			Name:      input.Name,
			Object:    object,
			Options: kubernetesresource.MutationOptions{
				DryRun:       input.DryRun,
				FieldManager: input.FieldManager,
			},
			Confirm:        input.Confirm,
			IdempotencyKey: input.IdempotencyKey,
		},
	)
	if err != nil {
		return Result{}, err
	}
	return resultFromObject(updated)
}

func decodeManifest(manifest []byte) (map[string]any, error) {
	if len(bytes.TrimSpace(manifest)) == 0 {
		return nil, ErrInvalidManifest
	}
	decoder := yaml.NewDecoder(bytes.NewReader(manifest))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil ||
		len(document.Content) != 1 ||
		document.Content[0].Kind != yaml.MappingNode ||
		validateYAMLNode(document.Content[0]) != nil {
		return nil, ErrInvalidManifest
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, ErrInvalidManifest
	}

	jsonValue, err := sigyaml.YAMLToJSONStrict(manifest)
	if err != nil {
		return nil, ErrInvalidManifest
	}
	var object map[string]any
	if err := json.Unmarshal(jsonValue, &object); err != nil || object == nil {
		return nil, ErrInvalidManifest
	}
	return object, nil
}

func validateYAMLNode(node *yaml.Node) error {
	if node == nil || node.Kind == yaml.AliasNode || node.Alias != nil ||
		node.Anchor != "" {
		return ErrInvalidManifest
	}
	switch node.Kind {
	case yaml.MappingNode:
		if len(node.Content)%2 != 0 {
			return ErrInvalidManifest
		}
		keys := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.Tag != "!!str" {
				return ErrInvalidManifest
			}
			if _, exists := keys[key.Value]; exists {
				return ErrInvalidManifest
			}
			keys[key.Value] = struct{}{}
			if err := validateYAMLNode(node.Content[index+1]); err != nil {
				return err
			}
		}
	case yaml.SequenceNode:
		for _, child := range node.Content {
			if err := validateYAMLNode(child); err != nil {
				return err
			}
		}
	case yaml.ScalarNode:
		// Kubernetes manifests are JSON-shaped. Reject explicit YAML-only
		// tags so conversion cannot silently change the submitted value.
		switch node.Tag {
		case "!!str", "!!bool", "!!int", "!!float", "!!null":
		default:
			return ErrInvalidManifest
		}
	default:
		return ErrInvalidManifest
	}
	return nil
}

func validateIdentity(
	input GetInput,
	submitted map[string]any,
	current map[string]any,
) error {
	wanted := unstructured.Unstructured{Object: submitted}
	live := unstructured.Unstructured{Object: current}
	if wanted.GetAPIVersion() == "" ||
		wanted.GetKind() == "" ||
		wanted.GetName() != input.Name ||
		wanted.GetNamespace() != input.Namespace ||
		wanted.GroupVersionKind().Group != input.Resource.Group ||
		wanted.GroupVersionKind().Version != input.Resource.Version ||
		wanted.GroupVersionKind() != live.GroupVersionKind() {
		return ErrInvalidManifest
	}
	if wanted.GetUID() == "" || live.GetUID() == "" ||
		wanted.GetUID() != live.GetUID() {
		return ErrResourceUIDChanged
	}
	if wanted.GetResourceVersion() == "" || live.GetResourceVersion() == "" ||
		wanted.GetResourceVersion() != live.GetResourceVersion() {
		return ErrResourceVersionChanged
	}
	return nil
}

func resultFromObject(object map[string]any) (Result, error) {
	value := unstructured.Unstructured{Object: object}
	jsonValue, err := json.Marshal(object)
	if err != nil {
		return Result{}, fmt.Errorf("%w: encode Kubernetes object", ErrInvalidManifest)
	}
	manifest, err := sigyaml.JSONToYAML(jsonValue)
	if err != nil {
		return Result{}, fmt.Errorf("%w: encode Kubernetes YAML", ErrInvalidManifest)
	}
	if len(manifest) == 0 || manifest[len(manifest)-1] != '\n' {
		manifest = append(manifest, '\n')
	}
	return Result{
		Manifest:        manifest,
		UID:             string(value.GetUID()),
		ResourceVersion: value.GetResourceVersion(),
	}, nil
}
