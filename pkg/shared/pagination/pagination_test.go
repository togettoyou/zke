package pagination

import (
	"errors"
	"testing"
)

func TestRequestValidate(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name    string
		request Request
		valid   bool
	}{
		{name: "default page", request: Request{Limit: DefaultLimit}, valid: true},
		{name: "maximum page", request: Request{Limit: MaxLimit, Offset: MaxOffset}, valid: true},
		{name: "zero limit", request: Request{Limit: 0}, valid: false},
		{name: "limit above maximum", request: Request{Limit: MaxLimit + 1}, valid: false},
		{name: "negative offset", request: Request{Limit: 10, Offset: -1}, valid: false},
		{name: "offset above maximum", request: Request{Limit: 10, Offset: MaxOffset + 1}, valid: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.request.Validate()
			if testCase.valid && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if !testCase.valid && !errors.Is(err, ErrInvalidPage) {
				t.Fatalf("Validate() = %v, want ErrInvalidPage", err)
			}
		})
	}
}

func TestNewResult(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name     string
		request  Request
		total    int
		returned int
		expected Result
	}{
		{
			name:     "first page of many",
			request:  Request{Limit: 10, Offset: 0},
			total:    25,
			returned: 10,
			expected: Result{Limit: 10, Offset: 0, Total: 25, HasMore: true},
		},
		{
			name:     "last page",
			request:  Request{Limit: 10, Offset: 20},
			total:    25,
			returned: 5,
			expected: Result{Limit: 10, Offset: 20, Total: 25, HasMore: false},
		},
		{
			name:     "empty result set",
			request:  Request{Limit: 10, Offset: 0},
			total:    0,
			returned: 0,
			expected: Result{Limit: 10, Offset: 0, Total: 0, HasMore: false},
		},
		{
			// Rows removed while the Console sat on the last page: the total
			// must still describe the filtered set, not the empty page.
			name:     "offset beyond the end still reports the total",
			request:  Request{Limit: 10, Offset: 90},
			total:    25,
			returned: 0,
			expected: Result{Limit: 10, Offset: 90, Total: 25, HasMore: false},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got := NewResult(testCase.request, testCase.total, testCase.returned)
			if got != testCase.expected {
				t.Fatalf("NewResult() = %+v, want %+v", got, testCase.expected)
			}
		})
	}
}
