package integrations

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/pkg/safehttp"
)

func TestGitHubEncryptionBoundToTenantAndPurpose(t *testing.T) {
	key := bytes.Repeat([]byte{2}, 32)
	a, b := uuid.New(), uuid.New()
	id := uuid.New()
	value := []byte(strings.Repeat("credential-", 4))
	sealed, e := seal(key, binding(a, id, "oauth"), value)
	if e != nil {
		t.Fatal(e)
	}
	own, e := unseal(key, binding(a, id, "oauth"), sealed)
	if e != nil || !bytes.Equal(own, value) {
		t.Fatal("roundtrip", e)
	}
	for _, aad := range []string{binding(b, id, "oauth"), binding(a, id, "webhook"), binding(a, uuid.New(), "oauth")} {
		if _, e = unseal(key, aad, sealed); e == nil {
			t.Fatal("swapped ciphertext accepted")
		}
	}
	sealed[len(sealed)-1] ^= 1
	if _, e = unseal(key, binding(a, id, "oauth"), sealed); e == nil {
		t.Fatal("tamper accepted")
	}
}
func TestGitHubRawBodySignature(t *testing.T) {
	secret := opaque()
	body := []byte(`{"repository":{"id":123}}`)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	signature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if e := VerifyGitHubSignature(secret, signature, body); e != nil {
		t.Fatal(e)
	}
	for _, sig := range []string{"", strings.Replace(signature, "sha256=", "sha1=", 1), "sha256=" + strings.Repeat("0", 64)} {
		if VerifyGitHubSignature(secret, sig, body) == nil {
			t.Fatal("forgery accepted")
		}
	}
	if VerifyGitHubSignature(secret, signature, append(body, ' ')) == nil {
		t.Fatal("modified body accepted")
	}
}

type githubTransport func(*http.Request) (*http.Response, error)

func (f githubTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestGitHubOAuthContractAndScopeDowngrade(t *testing.T) {
	for _, scope := range []string{"", "repo", "public_repo", "read:user"} {
		client := safehttp.NewClient(time.Second, false)
		client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://github.com/login/oauth/access_token" {
				t.Fatal("wrong endpoint")
			}
			if e := r.ParseForm(); e != nil {
				t.Fatal(e)
			}
			if r.Form.Get("code_verifier") != "test-verifier" || r.Form.Get("client_secret") != "test-private-value" {
				t.Fatal("missing PKCE or client authentication")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"fixture-value-long-enough","token_type":"bearer","scope":"` + scope + `"}`)), Header: make(http.Header)}, nil
		})
		c := &githubClient{config: GitHubConfig{ClientID: "client", ClientSecret: "test-private-value"}, http: client}
		_, e := c.Exchange(context.Background(), "code", "test-verifier", "https://app.example/api/integrations/github/callback")
		if (e == nil) != (scope == "") {
			t.Fatal("broader OAuth grant accepted", scope, e)
		}
	}
}

func TestGitHubWorkerHasNoProviderCredentials(t *testing.T) {
	w := NewGitHubWorker(nil)
	if len(w.Config.Key) != 0 || w.Config.ClientSecret != "" || w.Client != nil {
		t.Fatal("worker received unnecessary GitHub credentials")
	}
}

func TestGitHubRepositoryScopeChecks(t *testing.T) {
	for _, body := range []string{`{"id":123,"full_name":"octo/repo","private":false}`, `{"id":123,"full_name":"octo/repo","private":true}`, `{"id":123,"full_name":"other/repo","private":false}`} {
		client := safehttp.NewClient(time.Second, false)
		client.Transport = githubTransport(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "api.github.com" || r.URL.Path != "/repos/octo/repo" || r.Header.Get("Authorization") != "Bearer fixture-value" {
				t.Fatal("repository request escaped fixed endpoint")
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})
		c := &githubClient{http: client}
		_, e := c.Repository(context.Background(), "fixture-value", "octo/repo")
		want := strings.Contains(body, `"private":false`) && strings.Contains(body, `"full_name":"octo/repo"`)
		if (e == nil) != want {
			t.Fatal("wrong repository scope", e)
		}
	}
}
