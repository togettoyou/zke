// Package kubernetesmanifest applies and deletes whole Kubernetes manifests —
// what `kubectl apply -f` and `kubectl delete -f` do — over the Server's
// existing per-object resource path.
//
// Three things make it more than a loop over the single-object YAML API.
//
// A manifest names its objects by `apiVersion` and `kind`, while every API in
// the platform is addressed by group, version and resource, so the Cluster's own
// discovery has to answer which resource serves each Kind before anything can be
// sent anywhere.
//
// The documents in one file do not answer to one permission. A file holding a
// Deployment, a Secret and a RoleBinding touches three families that the typed
// APIs deliberately keep behind three different permissions, and a manifest
// endpoint guarded by a single route-level check would be a way around two of
// them. Every document is therefore decided on its own, and a file with one
// document the caller may not write is refused whole — no partial write.
//
// Kubernetes has no transaction. A manifest is applied object by object, so a
// failure halfway leaves the objects before it applied. That cannot be fixed,
// only reported honestly: this package plans first — resolving, checking
// permissions and running Kubernetes' own DryRun over every document before any
// of them is written — stops at the first real failure, and reports which
// documents were applied, which failed and which were never attempted.
package kubernetesmanifest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/togettoyou/zke/pkg/server/kubernetesresource"
	"github.com/togettoyou/zke/pkg/server/kubernetesyaml"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	// The manifest itself is unusable: it does not parse, holds no documents, or
	// holds more than the endpoint accepts.
	ErrInvalidManifest  = kubernetesyaml.ErrInvalidManifest
	ErrEmptyManifest    = kubernetesyaml.ErrEmptyManifest
	ErrTooManyDocuments = kubernetesyaml.ErrTooManyDocuments

	// One document cannot be turned into a request: no `apiVersion` or `kind`,
	// no name, a Kind the Cluster does not serve, a Namespace that contradicts
	// the one the request names, or a Namespace on a cluster-scoped object.
	ErrDocumentInvalid = errors.New("invalid Kubernetes manifest document")
	// Two documents name the same object. Applying both would make the result
	// depend on which came last, and deleting the same object twice reports a
	// failure for a delete that already succeeded.
	ErrDuplicateDocument = errors.New("Kubernetes manifest names the same object twice")
	// The Kind is not in the Cluster's discovery.
	ErrUnknownKind = errors.New("Kubernetes Kind is not served by the Cluster")
	// The Cluster serves the resource but not the verb this operation needs.
	ErrVerbUnsupported = errors.New("Kubernetes resource does not support the requested verb")
)

// Operation is which of the two things a manifest request does.
type Operation string

const (
	OperationApply  Operation = "apply"
	OperationDelete Operation = "delete"
)

// Action is what a document turned out to mean for the object it names, decided
// by reading the Cluster rather than by guessing from the document.
type Action string

const (
	ActionCreate Action = "create"
	ActionUpdate Action = "update"
	ActionDelete Action = "delete"
	// A delete naming an object that is not there. Reported rather than failed:
	// the file describes a desired absence, and the object is absent.
	ActionAbsent Action = "absent"
	// The document could not be resolved far enough to have an action.
	ActionUnknown Action = "unknown"
)

// Status is how far one document got.
type Status string

const (
	// Planned and permitted, not yet executed. Only a DryRun run reports it.
	StatusPlanned Status = "planned"
	// The caller's permissions do not cover this document. Nothing was sent.
	StatusRefused Status = "refused"
	// The document could not be turned into a request. Nothing was sent.
	StatusInvalid   Status = "invalid"
	StatusSucceeded Status = "succeeded"
	StatusSkipped   Status = "skipped"
	StatusFailed    Status = "failed"
	// A document after the one that failed. Named rather than omitted, because
	// "not attempted" and "succeeded" are the two things an operator must be
	// able to tell apart before running the file again.
	StatusNotAttempted Status = "not_attempted"
)

// Document is one object's account of itself, from parsing through execution.
type Document struct {
	// Position in the submitted manifest, counting only non-empty documents.
	Index      int
	APIVersion string
	Kind       string
	Namespace  string
	Name       string
	Action     Action
	Status     Status
	// Which permission this document answers to. Empty while the document is too
	// malformed to belong to a family.
	Requirement kubernetesresource.ManifestRequirement
	// Whether Kubernetes itself saw this document.
	//
	// False on a document a dry run could not send: a manifest that creates a
	// Namespace and then fills it cannot have the contents validated, because the
	// Namespace does not exist until the manifest is actually applied. Those
	// documents are still planned and still applied — refusing them would make the
	// most ordinary multi-document manifest there is impossible to apply — but the
	// operator has to be told which parts of the preview are a real answer from the
	// API Server and which are only "nothing could be checked yet".
	Previewed bool
	// The object's identity when it was read, carried into a delete as its
	// precondition and shown so an operator can tell which object was meant.
	UID             string
	ResourceVersion string
	// Before and After are internal material for an exact DryRun difference.
	// HTTP responses deliberately map Document field by field and never expose
	// either object; AIOps reduces them to bounded changed paths so Secret or
	// ConfigMap bodies do not become audit or trajectory content.
	Before map[string]any `json:"-"`
	After  map[string]any `json:"-"`
	// Why this document was refused, invalid or failed. Not serialized here: the
	// HTTP layer maps it to the same codes and messages every other Kubernetes
	// endpoint uses, so one failure does not have two vocabularies.
	Err error
}

// Result is the whole request's outcome.
type Result struct {
	DryRun bool
	// Every document's permission is covered. False means nothing was executed.
	Allowed bool
	// Every document could be turned into a request. False means nothing was
	// executed: a manifest with a document ZKE cannot send is a manifest the
	// operator is about to correct, and applying the rest of it first would leave
	// them correcting it against a Cluster that already holds half of it.
	Valid bool
	// At least one document failed, so execution stopped there.
	Failed bool
	// The Cluster's discovery was incomplete, so an unresolved Kind may exist in
	// the Cluster after all.
	CatalogPartial bool
	// In submitted order, whatever order they were executed in.
	Documents []Document
}

// Executable reports whether the manifest was carried out at all. Both halves
// have to hold: a document ZKE may not write and a document ZKE cannot send are
// different problems with the same answer, which is that nothing is written.
func (result Result) Executable() bool {
	return result.Allowed && result.Valid
}

// ResourceAccess is the manifest's view of the resource layer, narrowed to what
// this package needs. It is an interface so the planning and ordering rules can
// be tested without a Cluster; the only production implementation is
// kubernetesresource.ManifestAccess, which is also the only thing that can open
// the Secret and Namespace boundaries.
type ResourceAccess interface {
	DiscoverResources(
		context.Context,
		string,
	) (kubernetesresource.ManifestCatalog, error)
	GetResource(
		context.Context,
		kubernetesresource.ManifestGetInput,
	) (map[string]any, error)
	ApplyResource(
		context.Context,
		kubernetesresource.ManifestApplyInput,
	) (map[string]any, error)
	DeleteResource(
		context.Context,
		kubernetesresource.ManifestDeleteInput,
	) error
	RequirementForApply(
		kubernetesresource.ResourceIdentity,
		bool,
		...kubernetesresource.ManifestTarget,
	) (kubernetesresource.ManifestRequirement, bool, error)
	RequirementForDelete(
		kubernetesresource.ResourceIdentity,
		...kubernetesresource.ManifestTarget,
	) (kubernetesresource.ManifestRequirement, bool, error)
}

type Config struct {
	// Most documents one manifest may hold. Every document is a separate round
	// trip to the Agent, so this is what keeps one request from occupying a
	// Cluster's resource stream for minutes.
	MaxDocuments int
	// The field manager server-side Apply records ownership under. Fixed by the
	// Server rather than chosen by the client: Apply converges on repeated runs
	// only while the manager stays the same, and a client-chosen one would make
	// the second apply of a file orphan the fields the first one owned.
	FieldManager string
}

type Service struct {
	config Config
}

func NewService(config Config) *Service {
	return &Service{config: config}
}

type Input struct {
	ClusterID string
	Manifest  []byte
	// Fills in documents that name no Namespace, the way `kubectl -n` does. A
	// document that names a different one is refused rather than overridden:
	// silently moving an object to another Namespace is the one interpretation
	// nobody asks for.
	Namespace      string
	Operation      Operation
	DryRun         bool
	Force          bool
	Confirm        bool
	IdempotencyKey string
}

// Execute plans a manifest and, unless this is a DryRun, carries it out.
//
// The returned error is about the request as a whole — an unparseable manifest,
// a Cluster that could not be reached for discovery. Anything a single document
// ran into is on that document, because a manifest whose third object was
// rejected is not a failed request but a request with a result to read.
func (service *Service) Execute(
	ctx context.Context,
	access ResourceAccess,
	input Input,
) (Result, error) {
	if access == nil {
		return Result{}, kubernetesresource.ErrAgentUnsupported
	}
	if input.Operation != OperationApply && input.Operation != OperationDelete {
		return Result{}, kubernetesresource.ErrInvalidInput
	}
	if input.Namespace != "" && !validNamespaceName(input.Namespace) {
		return Result{}, kubernetesresource.ErrInvalidInput
	}
	objects, err := kubernetesyaml.DecodeDocuments(
		input.Manifest,
		service.config.MaxDocuments,
	)
	if err != nil {
		return Result{}, err
	}
	catalog, err := access.DiscoverResources(ctx, input.ClusterID)
	if err != nil {
		return Result{}, err
	}

	plan := service.plan(ctx, access, input, catalog, objects)
	result := Result{
		DryRun:         input.DryRun,
		CatalogPartial: catalog.Partial,
		Documents:      make([]Document, 0, len(plan)),
	}
	result.Allowed = true
	result.Valid = true
	for _, entry := range plan {
		result.Documents = append(result.Documents, entry.document)
		switch entry.document.Status {
		case StatusRefused:
			result.Allowed = false
		case StatusInvalid:
			result.Valid = false
		}
	}
	// One unusable document stops the whole request, including the DryRun, for
	// both reasons it can be unusable.
	//
	// A refusal, because executing the documents a caller may write while refusing
	// the rest would be a partial apply chosen by permissions rather than by the
	// operator, and the half that went through is the half nobody asked for on its
	// own.
	//
	// A document that could not be turned into a request, for the same reason
	// arrived at from the other side: a file with a misspelled Kind is a file the
	// operator is about to correct and submit again, and having applied nine of
	// its ten objects in the meantime makes that second submission a different
	// operation against a Cluster that is already half-changed.
	if !result.Executable() {
		return result, nil
	}
	service.execute(ctx, access, input, plan)
	for index, entry := range plan {
		result.Documents[index] = entry.document
		if entry.document.Status == StatusFailed {
			result.Failed = true
		}
	}
	return result, nil
}

// plannedDocument is a document plus what executing it needs, which the Document
// itself does not carry outside this package.
type plannedDocument struct {
	document Document
	resource kubernetesresource.ResourceIdentity
	// The verbs the Cluster reports for this resource, narrowed to the ones the
	// Server implements. Kept so an unsupported operation is reported against
	// the document instead of arriving as a rejection from the Agent.
	verbs   []string
	object  map[string]any
	current map[string]any
}

func (service *Service) plan(
	ctx context.Context,
	access ResourceAccess,
	input Input,
	catalog kubernetesresource.ManifestCatalog,
	objects []map[string]any,
) []*plannedDocument {
	plan := make([]*plannedDocument, 0, len(objects))
	seen := make(map[string]struct{}, len(objects))
	for index, object := range objects {
		entry := service.planDocument(
			ctx, access, input, catalog, index, object, seen,
		)
		plan = append(plan, entry)
	}
	return plan
}

func (service *Service) planDocument(
	ctx context.Context,
	access ResourceAccess,
	input Input,
	catalog kubernetesresource.ManifestCatalog,
	index int,
	object map[string]any,
	seen map[string]struct{},
) *plannedDocument {
	value := &unstructured.Unstructured{Object: object}
	document := Document{
		Index:      index,
		APIVersion: value.GetAPIVersion(),
		Kind:       value.GetKind(),
		Namespace:  value.GetNamespace(),
		Name:       value.GetName(),
		Action:     ActionUnknown,
		Status:     StatusInvalid,
	}
	entry := &plannedDocument{document: document, object: object}

	catalogEntry, namespace, err := resolveDocument(catalog, input, value)
	if err != nil {
		entry.document.Err = err
		return entry
	}
	entry.resource = catalogEntry.Resource
	entry.verbs = catalogEntry.Verbs
	entry.document.Namespace = namespace
	entry.document.Name = value.GetName()
	// Written back into the document, not just recorded beside it.
	//
	// The request's Namespace fills in documents that name none, and the object
	// that gets applied has to be the object that was resolved: the resource layer
	// checks the submitted body's own `metadata.namespace` against the scope of the
	// request before anything is sent, so a document still carrying no Namespace
	// would be refused as an identity mismatch — the Namespace an operator supplied
	// once for the whole file, arriving as an invalid-request error on every
	// document in it. Nothing else in the object is rewritten.
	if namespace != "" && value.GetNamespace() != namespace {
		value.SetNamespace(namespace)
	}

	identity := documentIdentity(catalogEntry.Resource, namespace, value.GetName())
	if _, exists := seen[identity]; exists {
		entry.document.Err = ErrDuplicateDocument
		return entry
	}
	seen[identity] = struct{}{}

	if input.Operation == OperationDelete {
		service.planDelete(ctx, access, input, entry)
		return entry
	}
	service.planApply(ctx, access, input, entry)
	return entry
}

func (service *Service) planApply(
	ctx context.Context,
	access ResourceAccess,
	input Input,
	entry *plannedDocument,
) {
	createRequirement, createAllowed, err := access.RequirementForApply(
		entry.resource, true, kubernetesresource.ManifestTarget{Namespace: entry.document.Namespace, Name: entry.document.Name},
	)
	if err != nil {
		entry.document.Status = StatusInvalid
		entry.document.Err = err
		return
	}
	updateRequirement, updateAllowed, _ := access.RequirementForApply(
		entry.resource, false, kubernetesresource.ManifestTarget{Namespace: entry.document.Namespace, Name: entry.document.Name},
	)
	// A family whose creating and changing collapse into one permission is decided
	// without reading the Cluster. Two reasons, and the second is the binding one:
	// the answer cannot depend on whether the object exists, and for a Secret the
	// read is itself guarded by the permission being checked — so a refused caller
	// must not reach it.
	//
	// The generic family is read first even when both halves are refused, because
	// only the read says which of the two permissions to name, and naming the wrong
	// one sends the operator to ask for a permission that would not have helped.
	// The read costs nothing they do not already hold: it answers to `cluster.read`,
	// which the route required before any of this ran.
	if createRequirement == updateRequirement && !createAllowed {
		entry.document.Status = StatusRefused
		entry.document.Requirement = createRequirement
		entry.document.Err = kubernetesresource.ErrManifestForbidden
		return
	}

	current, err := access.GetResource(ctx, kubernetesresource.ManifestGetInput{
		ClusterID: input.ClusterID,
		Resource:  entry.resource,
		Namespace: entry.document.Namespace,
		Name:      entry.document.Name,
	})
	switch {
	case err == nil:
		entry.current = current
		entry.document.Before = current
		live := &unstructured.Unstructured{Object: current}
		entry.document.UID = string(live.GetUID())
		entry.document.ResourceVersion = live.GetResourceVersion()
		entry.document.Action = ActionUpdate
		entry.document.Requirement = updateRequirement
		if !updateAllowed {
			entry.document.Status = StatusRefused
			entry.document.Err = kubernetesresource.ErrManifestForbidden
			return
		}
	case errors.Is(err, kubernetesresource.ErrResourceNotFound):
		entry.document.Action = ActionCreate
		entry.document.Requirement = createRequirement
		if !createAllowed {
			entry.document.Status = StatusRefused
			entry.document.Err = kubernetesresource.ErrManifestForbidden
			return
		}
	default:
		// The Cluster could not say whether the object exists, so neither the
		// permission nor the write can be decided. Reported on the document
		// rather than failing the request: the other documents' plans are still
		// worth showing.
		entry.document.Status = StatusFailed
		entry.document.Err = err
		return
	}
	if !hasVerb(entry, "patch") {
		entry.document.Status = StatusInvalid
		entry.document.Err = ErrVerbUnsupported
		return
	}
	entry.document.Status = StatusPlanned
}

func (service *Service) planDelete(
	ctx context.Context,
	access ResourceAccess,
	input Input,
	entry *plannedDocument,
) {
	requirement, allowed, err := access.RequirementForDelete(entry.resource, kubernetesresource.ManifestTarget{Namespace: entry.document.Namespace, Name: entry.document.Name})
	if err != nil {
		entry.document.Status = StatusInvalid
		entry.document.Err = err
		return
	}
	entry.document.Requirement = requirement
	if !allowed {
		entry.document.Status = StatusRefused
		entry.document.Err = kubernetesresource.ErrManifestForbidden
		return
	}
	current, err := access.GetResource(ctx, kubernetesresource.ManifestGetInput{
		ClusterID: input.ClusterID,
		Resource:  entry.resource,
		Namespace: entry.document.Namespace,
		Name:      entry.document.Name,
	})
	if errors.Is(err, kubernetesresource.ErrResourceNotFound) {
		// Nothing to delete is not a failure. The manifest asked for the object
		// to be gone and it is gone; saying so is more useful than an error the
		// operator has to decide to ignore.
		entry.document.Action = ActionAbsent
		entry.document.Status = StatusSkipped
		return
	}
	if err != nil {
		entry.document.Status = StatusFailed
		entry.document.Err = err
		return
	}
	entry.current = current
	entry.document.Before = current
	live := &unstructured.Unstructured{Object: current}
	entry.document.UID = string(live.GetUID())
	entry.document.ResourceVersion = live.GetResourceVersion()
	entry.document.Action = ActionDelete
	if !hasVerb(entry, "delete") {
		entry.document.Status = StatusInvalid
		entry.document.Err = ErrVerbUnsupported
		return
	}
	entry.document.Status = StatusPlanned
}

// execute runs the planned documents, in the order the operation calls for, and
// stops at the first failure.
//
// Apply follows the file. Delete runs backwards through it, because a manifest
// is written so that what things live in comes before what lives in them — the
// Namespace, then the workloads — and removing it in that order would take the
// dependents with the container before the file ever names them.
//
// Stopping at the first failure rather than pressing on: the objects in one file
// are usually one thing, and continuing past a rejected object tends to build
// the rest of a system around a piece that is missing. What was already written
// stays written, because rolling it back would be a second set of destructive
// operations chosen by the Server rather than by the operator — the result names
// every document instead, so the file can be corrected and applied again.
func (service *Service) execute(
	ctx context.Context,
	access ResourceAccess,
	input Input,
	plan []*plannedDocument,
) {
	order := make([]*plannedDocument, len(plan))
	copy(order, plan)
	if input.Operation == OperationDelete {
		slices.Reverse(order)
	}
	unpreviewable := unpreviewableNamespaces(input, plan)
	stopped := false
	for _, entry := range order {
		if entry.document.Status != StatusPlanned {
			continue
		}
		if stopped {
			entry.document.Status = StatusNotAttempted
			continue
		}
		if _, pending := unpreviewable[entry.document.Namespace]; pending {
			// Nothing to send: the API Server would reject it for a Namespace that
			// this dry run did not create either. Left planned and unpreviewed
			// rather than failed — see unpreviewableNamespaces.
			entry.document.Status = StatusPlanned
			entry.document.Previewed = false
			continue
		}
		if err := service.executeDocument(ctx, access, input, entry); err != nil {
			entry.document.Status = StatusFailed
			entry.document.Err = err
			stopped = true
			continue
		}
		entry.document.Previewed = true
		if input.DryRun {
			// A dry run that reached the API Server and was accepted is a document
			// that is ready, not one that happened. Reporting it as succeeded read
			// as if the object had been written, which is the one thing a dry run
			// must never appear to have done.
			entry.document.Status = StatusPlanned
			continue
		}
		entry.document.Status = StatusSucceeded
	}
}

// unpreviewableNamespaces reports the Namespaces a dry run cannot validate
// anything inside.
//
// A manifest that creates a Namespace and then fills it is the most ordinary
// multi-document manifest there is, and server-side dry run cannot check any of
// it: the dry-run creation of the Namespace persists nothing, so every later
// document is submitted into a Namespace that does not exist and comes back as
// `not found` — a Kubernetes answer about the Namespace, arriving as a failure
// attributed to the ConfigMap. Left as failures, they also stopped the run at
// the second document, which made such a manifest impossible to apply through
// ZKE at all.
//
// So those documents are not sent during a dry run, and are reported as planned
// but unpreviewed. That is a real loss of checking, and it is stated rather than
// hidden: the alternative is refusing the manifest outright. The same limit
// applies to a CustomResourceDefinition and its own custom resources in one file;
// that case is not detected yet and still surfaces as a dry-run failure.
//
// Only for a dry run, and only for Namespaces this manifest is about to create —
// a Namespace that already exists validates normally. Deletes are unaffected:
// they run in reverse, so an object's Namespace is still there when it goes.
func unpreviewableNamespaces(
	input Input,
	plan []*plannedDocument,
) map[string]struct{} {
	pending := map[string]struct{}{}
	if !input.DryRun || input.Operation != OperationApply {
		return pending
	}
	for _, entry := range plan {
		if entry.resource.Group == "" &&
			entry.resource.Resource == "namespaces" &&
			entry.document.Action == ActionCreate &&
			entry.document.Status == StatusPlanned {
			pending[entry.document.Name] = struct{}{}
		}
	}
	return pending
}

func (service *Service) executeDocument(
	ctx context.Context,
	access ResourceAccess,
	input Input,
	entry *plannedDocument,
) error {
	key := service.documentIdempotencyKey(input, entry)
	if input.Operation == OperationDelete {
		return access.DeleteResource(ctx, kubernetesresource.ManifestDeleteInput{
			ClusterID:      input.ClusterID,
			Resource:       entry.resource,
			Namespace:      entry.document.Namespace,
			Name:           entry.document.Name,
			Current:        entry.current,
			DryRun:         input.DryRun,
			Confirm:        input.Confirm,
			IdempotencyKey: key,
		})
	}
	applied, err := access.ApplyResource(ctx, kubernetesresource.ManifestApplyInput{
		ClusterID:      input.ClusterID,
		Resource:       entry.resource,
		Namespace:      entry.document.Namespace,
		Name:           entry.document.Name,
		Object:         entry.object,
		Current:        entry.current,
		DryRun:         input.DryRun,
		Force:          input.Force,
		Confirm:        input.Confirm,
		FieldManager:   service.config.FieldManager,
		IdempotencyKey: key,
	})
	if err != nil {
		return err
	}
	entry.document.After = applied
	live := &unstructured.Unstructured{Object: applied}
	entry.document.UID = string(live.GetUID())
	entry.document.ResourceVersion = live.GetResourceVersion()
	return nil
}

// documentIdempotencyKey derives one document's key from the request's.
//
// Derived rather than reused: the Agent's replay cache keys on the request, and
// sending thirty different objects under one key would make the second document
// look like a repeat of the first. Derived from the object's identity rather
// than its position, so that reordering a file does not hand one object the key
// another was written under. A DryRun gets its own key space for the same
// reason: it is a different request against the same object.
func (service *Service) documentIdempotencyKey(
	input Input,
	entry *plannedDocument,
) string {
	stage := "execute"
	if input.DryRun {
		stage = "dry-run"
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		input.IdempotencyKey,
		string(input.Operation),
		stage,
		documentIdentity(
			entry.resource,
			entry.document.Namespace,
			entry.document.Name,
		),
	}, "\x00")))
	return hex.EncodeToString(sum[:])
}

// resolveDocument turns a document into the request it names, or says why it
// cannot be one.
func resolveDocument(
	catalog kubernetesresource.ManifestCatalog,
	input Input,
	value *unstructured.Unstructured,
) (kubernetesresource.ManifestCatalogEntry, string, error) {
	empty := kubernetesresource.ManifestCatalogEntry{}
	apiVersion := value.GetAPIVersion()
	kind := value.GetKind()
	if apiVersion == "" || kind == "" || value.GetName() == "" {
		return empty, "", ErrDocumentInvalid
	}
	// A name the Server generates is a name the operator cannot write down, and
	// a manifest whose objects are named on the way in cannot be applied twice
	// to the same objects — which is the whole of what apply means.
	if value.GetGenerateName() != "" {
		return empty, "", ErrDocumentInvalid
	}
	groupVersion, err := schema.ParseGroupVersion(apiVersion)
	if err != nil || groupVersion.Version == "" {
		return empty, "", ErrDocumentInvalid
	}
	entry, exists := catalog.Lookup(kubernetesresource.ManifestKind{
		Group:   groupVersion.Group,
		Version: groupVersion.Version,
		Kind:    kind,
	})
	if !exists {
		return empty, "", ErrUnknownKind
	}

	namespace := value.GetNamespace()
	switch {
	case !entry.Namespaced:
		// A Namespace on a cluster-scoped object is refused rather than dropped:
		// it means the document was written about something else, and applying
		// it anyway writes an object the operator did not describe. The request's
		// own default is not applied to these at all.
		if namespace != "" {
			return empty, "", ErrDocumentInvalid
		}
	case namespace == "":
		if input.Namespace == "" {
			return empty, "", ErrDocumentInvalid
		}
		namespace = input.Namespace
	case input.Namespace != "" && namespace != input.Namespace:
		return empty, "", ErrDocumentInvalid
	}
	if namespace != "" && !validNamespaceName(namespace) {
		return empty, "", ErrDocumentInvalid
	}
	return entry, namespace, nil
}

func hasVerb(entry *plannedDocument, verb string) bool {
	return slices.Contains(entry.verbs, verb)
}

func documentIdentity(
	resource kubernetesresource.ResourceIdentity,
	namespace string,
	name string,
) string {
	return fmt.Sprintf(
		"%s/%s/%s/%s/%s",
		resource.Group,
		resource.Version,
		resource.Resource,
		namespace,
		name,
	)
}

// A Namespace name, by Kubernetes' own rule for one. Checked here so a document
// naming an impossible Namespace is reported against that document instead of
// arriving as a rejection with no document attached.
func validNamespaceName(value string) bool {
	if len(value) == 0 || len(value) > 63 {
		return false
	}
	for index, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= '0' && character <= '9':
		case character == '-' && index != 0 && index != len(value)-1:
		default:
			return false
		}
	}
	return true
}
