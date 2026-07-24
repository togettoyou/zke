package validation

import "testing"

func TestIsUUID(t *testing.T) {
	t.Parallel()

	if !IsUUID("01234567-89ab-cdef-0123-456789abcdef") {
		t.Fatal("IsUUID() rejected a UUID")
	}
	for _, invalid := range []string{
		"",
		"not-a-uuid",
		"01234567-89ab-cdef-0123-456789abcdeg",
		"0123456789abcdef0123456789abcdef",
	} {
		if IsUUID(invalid) {
			t.Fatalf("IsUUID(%q) accepted an invalid UUID", invalid)
		}
	}
}
