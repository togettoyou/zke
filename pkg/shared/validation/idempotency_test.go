package validation

import "testing"

func TestIsIdempotencyKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "minimum length", value: "0123456789abcdef", valid: true},
		{name: "supported separators", value: "agent.enroll:key_01", valid: true},
		{name: "too short", value: "too-short", valid: false},
		{name: "surrounding whitespace", value: " 0123456789abcdef", valid: false},
		{name: "unsupported character", value: "0123456789abcde/", valid: false},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := IsIdempotencyKey(test.value); got != test.valid {
				t.Fatalf("IsIdempotencyKey(%q) = %t, want %t", test.value, got, test.valid)
			}
		})
	}
}
