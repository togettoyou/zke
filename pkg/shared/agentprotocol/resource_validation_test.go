package agentprotocol

import (
	"errors"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
	"google.golang.org/protobuf/proto"
)

func TestValidateResourceDiscoverRequest(t *testing.T) {
	t.Parallel()

	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
		RequestId:       "00000000-0000-4000-8000-000000000001",
		TimeoutMillis:   1000,
	}
	if err := validateResourceRequest(
		header,
		&agentv1.ResourceRequest{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
		},
		DefaultMaxResourceBodySize,
	); err != nil {
		t.Fatalf("valid Discover request rejected: %v", err)
	}

	testCases := []*agentv1.ResourceRequest{
		{
			Verb: agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
			Resource: &agentv1.GroupVersionResource{
				Version:  "v1",
				Resource: "pods",
			},
		},
		{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		},
		{
			Verb:     agentv1.ResourceVerb_RESOURCE_VERB_DISCOVER,
			BodySize: 1,
		},
	}
	for _, request := range testCases {
		if err := validateResourceRequest(
			header,
			request,
			DefaultMaxResourceBodySize,
		); !errors.Is(err, ErrStreamProtocol) {
			t.Errorf(
				"invalid Discover request error = %v, want protocol error: %+v",
				err,
				request,
			)
		}
	}
}

func TestValidateResourceMutationRequests(t *testing.T) {
	t.Parallel()

	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
		RequestId:       "00000000-0000-4000-8000-000000000001",
		TimeoutMillis:   1000,
		IdempotencyKey:  "0123456789abcdef",
	}
	resource := &agentv1.GroupVersionResource{
		Version:  "v1",
		Resource: "configmaps",
	}
	valid := []*agentv1.ResourceRequest{
		{
			Verb:            agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
			Resource:        resource,
			Namespace:       "zke-test",
			Representation:  agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			BodySize:        10,
			MutationOptions: &agentv1.MutationOptions{},
		},
		{
			Verb:           agentv1.ResourceVerb_RESOURCE_VERB_PATCH,
			Resource:       resource,
			Namespace:      "zke-test",
			Name:           "example",
			Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			PatchType:      agentv1.PatchType_PATCH_TYPE_APPLY,
			BodySize:       10,
			MutationOptions: &agentv1.MutationOptions{
				FieldManager: "zke-server",
			},
		},
		{
			Verb:      agentv1.ResourceVerb_RESOURCE_VERB_DELETE,
			Resource:  resource,
			Namespace: "zke-test",
			Name:      "example",
			DeleteOptions: &agentv1.DeleteOptions{
				Propagation: agentv1.DeletePropagation_DELETE_PROPAGATION_BACKGROUND,
				Preconditions: &agentv1.ResourcePreconditions{
					Uid: "example-uid",
				},
			},
		},
	}
	for _, request := range valid {
		if err := validateResourceRequest(
			header,
			request,
			DefaultMaxResourceBodySize,
		); err != nil {
			t.Errorf("valid mutation rejected: %v request=%+v", err, request)
		}
	}

	invalidHeader := proto.Clone(header).(*agentv1.StreamHeader)
	invalidHeader.IdempotencyKey = "short"
	if err := validateResourceRequest(
		invalidHeader,
		valid[0],
		DefaultMaxResourceBodySize,
	); !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("short idempotency key error = %v", err)
	}
	invalidPatch := proto.Clone(valid[1]).(*agentv1.ResourceRequest)
	invalidPatch.PatchType = agentv1.PatchType_PATCH_TYPE_MERGE
	invalidPatch.MutationOptions = &agentv1.MutationOptions{Force: true}
	if err := validateResourceRequest(
		header,
		invalidPatch,
		DefaultMaxResourceBodySize,
	); !errors.Is(err, ErrStreamProtocol) {
		t.Fatalf("force on merge patch error = %v", err)
	}
}

func TestValidateResourceRequestAllowsOnlyDedicatedPodEviction(t *testing.T) {
	t.Parallel()

	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
		RequestId:       "00000000-0000-4000-8000-000000000001",
		TimeoutMillis:   1000,
		IdempotencyKey:  "0123456789abcdef",
	}
	eviction := &agentv1.ResourceRequest{
		Verb: agentv1.ResourceVerb_RESOURCE_VERB_CREATE,
		Resource: &agentv1.GroupVersionResource{
			Version: "v1", Resource: "pods",
		},
		Namespace:         "zke-test",
		Name:              "api-123",
		Subresource:       "eviction",
		Representation:    agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
		BodySize:          10,
		MutationOptions:   &agentv1.MutationOptions{DryRun: true},
		PodEvictionAccess: true,
	}
	if err := validateResourceRequest(
		header,
		eviction,
		DefaultMaxResourceBodySize,
	); err != nil {
		t.Fatalf("valid Pod eviction rejected: %v", err)
	}

	testCases := []struct {
		name   string
		mutate func(*agentv1.ResourceRequest)
	}{
		{
			name: "dedicated access missing",
			mutate: func(request *agentv1.ResourceRequest) {
				request.PodEvictionAccess = false
			},
		},
		{
			name: "different subresource",
			mutate: func(request *agentv1.ResourceRequest) {
				request.Subresource = "exec"
			},
		},
		{
			name: "different resource",
			mutate: func(request *agentv1.ResourceRequest) {
				request.Resource.Resource = "services"
			},
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			request := proto.Clone(eviction).(*agentv1.ResourceRequest)
			testCase.mutate(request)
			if err := validateResourceRequest(
				header,
				request,
				DefaultMaxResourceBodySize,
			); !errors.Is(err, ErrStreamProtocol) {
				t.Fatalf("invalid Pod eviction error = %v, want protocol error", err)
			}
		})
	}
}

func TestValidateResourceRequestRejectsUnsafeIdentitySegments(t *testing.T) {
	t.Parallel()

	header := &agentv1.StreamHeader{
		ProtocolVersion: ProtocolVersion,
		Kind:            agentv1.StreamKind_STREAM_KIND_RESOURCE,
		RequestId:       "00000000-0000-4000-8000-000000000001",
		TimeoutMillis:   1000,
	}
	testCases := []struct {
		name    string
		request *agentv1.ResourceRequest
	}{
		{
			name: "path separator",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods/log",
				},
				Name:           "example",
				Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			},
		},
		{
			name: "query delimiter",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_GET,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
				Name:           "example?watch=1",
				Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			},
		},
		{
			name: "surrounding whitespace",
			request: &agentv1.ResourceRequest{
				Verb: agentv1.ResourceVerb_RESOURCE_VERB_LIST,
				Resource: &agentv1.GroupVersionResource{
					Version: "v1", Resource: "pods",
				},
				Namespace:      " tenant-a",
				Representation: agentv1.ResourceRepresentation_RESOURCE_REPRESENTATION_FULL_OBJECT,
			},
		},
	}
	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := validateResourceRequest(
				header,
				testCase.request,
				DefaultMaxResourceBodySize,
			); !errors.Is(err, ErrStreamProtocol) {
				t.Fatalf("validateResourceRequest() error = %v, want protocol error", err)
			}
		})
	}
}
