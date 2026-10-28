package securityspec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEveryOpenAPIOperationHasSecurityCase(t *testing.T) {
	ops, e := Load("../../..")
	if e != nil {
		t.Fatal(e)
	}
	if len(ops) < 50 {
		t.Fatal("incomplete OpenAPI inventory")
	}
}

func TestGatedFeaturesHaveNoHTTPIngress(t *testing.T) {
	ops, e := Load("../../..")
	if e != nil {
		t.Fatal(e)
	}
	for _, op := range ops {
		if strings.HasPrefix(op.Path, "/hooks/") && op.ID != "receiveGitHub" {
			t.Errorf("unreviewed vendor webhook ingress: %s", op.Path)
		}
		for _, prefix := range []string{"/oauth", "/webhooks", "/exports", "/auth/password-reset", "/auth/forgot-password"} {
			if strings.HasPrefix(op.Path, prefix) {
				t.Errorf("gated feature needs its threat controls and tests before exposure: %s", op.Path)
			}
		}
	}
}
func TestNewEndpointFailsCoverageGate(t *testing.T) {
	dir := t.TempDir()
	if e := os.Mkdir(filepath.Join(dir, "api"), 0700); e != nil {
		t.Fatal(e)
	}
	spec := filepath.Join(dir, "api", "openapi.yaml")
	cases := filepath.Join(dir, "api", "security-coverage.json")
	if e := os.WriteFile(spec, []byte("paths:\n  /new:\n    get:\n      operationId: newEndpoint\n"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(cases, []byte("[]"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := Load(dir); e == nil {
		t.Fatal("uncovered endpoint accepted")
	}
}
