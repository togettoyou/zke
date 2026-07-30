package store

import "testing"

func TestValidClusterAuditResultAllowsExternalMutationSuccess(t *testing.T) {
	t.Parallel()

	for _, result := range []string{"succeeded", "failed", "denied"} {
		if !validClusterAuditResult(result) {
			t.Errorf("validClusterAuditResult(%q) = false", result)
		}
	}
	for _, result := range []string{"", "success", "unknown"} {
		if validClusterAuditResult(result) {
			t.Errorf("validClusterAuditResult(%q) = true", result)
		}
	}
}
