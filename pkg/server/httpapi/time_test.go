package httpapi

import (
	"encoding/json"
	"testing"
	"time"
)

func TestResponseTimeUsesUTC8(t *testing.T) {
	t.Parallel()

	source := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	normalized := responseTime(source)
	assertUTC8Time(t, "normalized", normalized)
	if !normalized.Equal(source) {
		t.Fatalf("responseTime() changed instant: got %s, want %s", normalized, source)
	}
	encoded, err := json.Marshal(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `"2030-01-02T11:04:05+08:00"`; got != want {
		t.Fatalf("responseTime() JSON = %s, want %s", got, want)
	}
	if responseTimePointer(nil) != nil {
		t.Fatal("responseTimePointer(nil) is not nil")
	}
}

func assertUTC8Time(t *testing.T, field string, value time.Time) {
	t.Helper()
	_, offset := value.Zone()
	if offset != utc8OffsetSeconds {
		t.Fatalf("%s offset = %d, want %d: %s", field, offset, utc8OffsetSeconds, value)
	}
}

func assertUTC8TimePointer(t *testing.T, field string, value *time.Time) {
	t.Helper()
	if value == nil {
		t.Fatalf("%s is nil", field)
	}
	assertUTC8Time(t, field, *value)
}
