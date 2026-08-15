// Package buildinfo reports the version stamped into a ZKE binary at link
// time. Both zke-server and zke-agent read their version from here so that the
// two never disagree about what a version string is allowed to look like.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// DevelopmentVersion is reported when no version was stamped in and the module
// build information carries none either — a plain `go run ./cmd/zke-agent`.
// It is a value, not an empty string, so that logs and the Console always have
// something to show.
const DevelopmentVersion = "development"

// MaxVersionLength bounds what this package will report. The Server already
// refuses an Agent version longer than this on the control stream, so a binary
// stamped with something absurd should fail the same way on both sides rather
// than only at the far end of the connection.
const MaxVersionLength = 128

// version is set at link time and must stay a variable rather than a constant:
//
//	go build -ldflags "-X github.com/togettoyou/zke/pkg/shared/buildinfo.version=$(git describe --tags --always --dirty)" ./cmd/zke-server
//
// `git describe` prints the tag when the build sits exactly on one and falls
// back to the abbreviated commit otherwise, which is why a tagged release and
// an ordinary working build need no different command.
var version string

// Version returns the version of the running binary.
//
// Version is reported to the Server, written to logs and shown in the Console.
// It carries no meaning to any protocol decision: a Server and an Agent that
// disagree still speak to each other exactly as before.
func Version() string {
	if stamped := normalize(version); stamped != "" {
		return stamped
	}
	// `go install github.com/togettoyou/zke/cmd/zke-agent@v0.1.0` stamps no
	// ldflags but does record the module version, so prefer that over calling
	// such a build "development".
	if buildInfo, ok := debug.ReadBuildInfo(); ok {
		if main := normalize(buildInfo.Main.Version); main != "" && main != "(devel)" {
			return main
		}
	}
	return DevelopmentVersion
}

// normalize returns the empty string for anything unfit to report. Surrounding
// whitespace is rejected rather than trimmed: a version that arrives padded
// means the build pipeline substituted something unexpected, and quietly
// repairing it would hide that from whoever reads the value later.
func normalize(value string) string {
	if value == "" || len(value) > MaxVersionLength {
		return ""
	}
	if value != strings.TrimSpace(value) {
		return ""
	}
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		return ""
	}
	return value
}
