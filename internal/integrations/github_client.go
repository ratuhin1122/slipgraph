package integrations

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/slipgraph/slipgraph/internal/platform/config"
	"github.com/slipgraph/slipgraph/pkg/safehttp"
)

var ErrGitHub = errors.New("GitHub integration unavailable or invalid")
var repositoryName = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$`)

type GitHubConfig struct {
	worker                         bool
	ClientID, ClientSecret, Origin string
	Key                            []byte
}

func GitHubFromEnv() (GitHubConfig, error) {
	c := GitHubConfig{Origin: config.PublicOrigin()}
	var e error
	c.ClientID, e = config.Secret("GITHUB_CLIENT_ID", "GITHUB_CLIENT_ID_FILE")
	if e != nil {
		return c, e
	}
	c.ClientSecret, e = config.Secret("GITHUB_CLIENT_SECRET", "GITHUB_CLIENT_SECRET_FILE")
	if e != nil {
		return c, e
	}
	raw, e := config.Secret("GITHUB_ENCRYPTION_KEY", "GITHUB_ENCRYPTION_KEY_FILE")
	if e != nil {
		return c, e
	}
	if c.ClientID == "" && c.ClientSecret == "" && raw == "" {
		return c, nil
	}
	c.Key, e = hex.DecodeString(raw)
	if e != nil || len(c.Key) != 32 || c.ClientID == "" || c.ClientSecret == "" {
		return c, ErrGitHub
	}
	return c, nil
}
func (c GitHubConfig) enabled() bool {
	return len(c.Key) == 32 && c.ClientID != "" && c.ClientSecret != ""
}
func opaque() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func hashBytes(v string) []byte { h := sha256.Sum256([]byte(v)); return h[:] }
func digest(v string) string    { h := sha256.Sum256([]byte(v)); return hex.EncodeToString(h[:]) }
func seal(key []byte, aad string, value []byte) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, ErrGitHub
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, ErrGitHub
	}
	n := make([]byte, g.NonceSize())
	if _, e = rand.Read(n); e != nil {
		return nil, e
	}
	return g.Seal(n, n, value, []byte(aad)), nil
}
func unseal(key []byte, aad string, value []byte) ([]byte, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, ErrGitHub
	}
	g, e := cipher.NewGCM(b)
	if e != nil || len(value) < g.NonceSize() {
		return nil, ErrGitHub
	}
	out, e := g.Open(nil, value[:g.NonceSize()], value[g.NonceSize():], []byte(aad))
	if e != nil {
		return nil, ErrGitHub
	}
	return out, nil
}
func binding(workspace, connection uuid.UUID, purpose string) string {
	return "slipgraph:github:v1:" + workspace.String() + ":" + connection.String() + ":" + purpose
}

type GitHubClient interface {
	Exchange(context.Context, string, string, string) (string, error)
	Repository(context.Context, string, string) (int64, error)
}
type githubClient struct {
	config GitHubConfig
	http   *http.Client
}

func newGitHubClient(c GitHubConfig) GitHubClient {
	return &githubClient{config: c, http: safehttp.NewClient(15*time.Second, false)}
}
func (c *githubClient) Exchange(ctx context.Context, code, verifier, redirect string) (string, error) {
	data := url.Values{"client_id": {c.config.ClientID}, "client_secret": {c.config.ClientSecret}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://github.com/login/oauth/access_token", strings.NewReader(data.Encode()))
	if e != nil {
		return "", ErrGitHub
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	var result struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		TokenType   string `json:"token_type"`
	}
	if e = c.do(req, &result); e != nil || result.Scope != "" || result.TokenType != "bearer" || len(result.AccessToken) < 10 || len(result.AccessToken) > 1024 {
		return "", ErrGitHub
	}
	return result.AccessToken, nil
}
func (c *githubClient) Repository(ctx context.Context, token, name string) (int64, error) {
	if !repositoryName.MatchString(name) {
		return 0, ErrGitHub
	}
	req, e := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/repos/"+name, nil)
	if e != nil {
		return 0, ErrGitHub
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	var result struct {
		ID       int64  `json:"id"`
		FullName string `json:"full_name"`
		Private  bool   `json:"private"`
	}
	if e = c.do(req, &result); e != nil || result.ID <= 0 || result.Private || !strings.EqualFold(name, result.FullName) {
		return 0, ErrGitHub
	}
	return result.ID, nil
}
func (c *githubClient) do(req *http.Request, target interface{}) error {
	response, e := c.http.Do(req)
	if e != nil {
		return ErrGitHub
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != 200 {
		return ErrGitHub
	}
	data, e := io.ReadAll(io.LimitReader(response.Body, 1<<20+1))
	if e != nil || len(data) > 1<<20 {
		return ErrGitHub
	}
	if e = json.NewDecoder(bytes.NewReader(data)).Decode(target); e != nil {
		return ErrGitHub
	}
	return nil
}

// GitHub signs precisely the raw body, not timestamp or delivery headers.
func VerifyGitHubSignature(secret, header string, body []byte) error {
	if len(secret) != 43 || !strings.HasPrefix(header, "sha256=") || len(body) > 1<<20 {
		return ErrWebhook
	}
	supplied, e := hex.DecodeString(strings.TrimPrefix(header, "sha256="))
	if e != nil || len(supplied) != 32 {
		return ErrWebhook
	}
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	if !hmac.Equal(mac.Sum(nil), supplied) {
		return ErrWebhook
	}
	return nil
}
