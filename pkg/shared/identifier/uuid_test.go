package identifier

import (
	"strings"
	"testing"

	"github.com/togettoyou/zke/pkg/shared/validation"
)

func TestNewUUID(t *testing.T) {
	t.Parallel()

	seen := make(map[string]struct{}, 64)
	for range 64 {
		value, err := NewUUID()
		if err != nil {
			t.Fatal(err)
		}
		if !validation.IsUUID(value) {
			t.Fatalf("NewUUID() = %q, want UUID", value)
		}
		if value[14] != '4' || !strings.ContainsRune("89ab", rune(value[19])) {
			t.Fatalf("NewUUID() = %q, want RFC 4122 version 4 UUID", value)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("NewUUID() returned duplicate %q", value)
		}
		seen[value] = struct{}{}
	}
}
