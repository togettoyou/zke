package buildinfo

import (
	"strings"
	"testing"
)

func TestVersionReportsStampedValue(t *testing.T) {
	// Not parallel: these cases write the package-level stamp that Version reads.
	for _, testCase := range []struct {
		name    string
		stamped string
		want    string
	}{
		{name: "release tag", stamped: "v0.3.1", want: "v0.3.1"},
		{name: "commits past a tag", stamped: "v0.3.1-4-gad5797d", want: "v0.3.1-4-gad5797d"},
		{name: "untagged commit", stamped: "ad5797d", want: "ad5797d"},
		{name: "dirty tree", stamped: "ad5797d-dirty", want: "ad5797d-dirty"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(restoreVersion(version))
			version = testCase.stamped
			if got := Version(); got != testCase.want {
				t.Fatalf("Version() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestVersionRejectsUnusableStamp(t *testing.T) {
	// A stamp the Server would refuse must not reach the control stream. Test
	// binaries carry no module version either, so the fallback is the constant.
	for _, testCase := range []struct {
		name    string
		stamped string
	}{
		{name: "empty", stamped: ""},
		{name: "padded", stamped: " v0.3.1 "},
		{name: "newline", stamped: "v0.3.1\n"},
		{name: "control character", stamped: "v0.3.1\x00"},
		{name: "too long", stamped: strings.Repeat("v", MaxVersionLength+1)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Cleanup(restoreVersion(version))
			version = testCase.stamped
			if got := Version(); got != DevelopmentVersion {
				t.Fatalf("Version() = %q, want %q", got, DevelopmentVersion)
			}
		})
	}
}

func TestVersionIsSafeToReport(t *testing.T) {
	t.Parallel()

	reported := Version()
	if reported == "" || len(reported) > MaxVersionLength {
		t.Fatalf("Version() = %q, want a non-empty value of at most %d bytes", reported, MaxVersionLength)
	}
	if reported != strings.TrimSpace(reported) {
		t.Fatalf("Version() = %q, want no surrounding whitespace", reported)
	}
}

func restoreVersion(previous string) func() {
	return func() { version = previous }
}
