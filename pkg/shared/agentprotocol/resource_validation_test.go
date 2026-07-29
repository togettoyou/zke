package agentprotocol

import (
	"errors"
	"testing"

	agentv1 "github.com/togettoyou/zke/api/agent/v1"
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
