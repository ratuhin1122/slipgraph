package auth

import (
	"bytes"
	"testing"
)

func TestPasswordHash(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "correct horse battery staple") || VerifyPassword(hash, "incorrect password") {
		t.Fatal("password verification failed")
	}
	second, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if second == hash {
		t.Fatal("salt reused")
	}
	for _, invalid := range []string{"$argon2id$v=19$m=999999999,t=3,p=2$bad$bad", "$argon2i$v=19$m=65536,t=3,p=2$bad$bad", "invalid"} {
		if VerifyPassword(invalid, "password") {
			t.Fatal("malformed hash accepted")
		}
	}
	if _, err := HashPassword("short"); err == nil {
		t.Fatal("weak password accepted")
	}
}
func TestTokens(t *testing.T) {
	a, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	b, err := randomToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 43 || a == b || bytes.Equal(tokenHash(a), []byte(a)) {
		t.Fatal("invalid random token")
	}
}
func TestCSRF(t *testing.T) {
	s := &Service{key: bytes.Repeat([]byte{3}, 32)}
	token, _ := randomToken()
	header := s.CSRF(token)
	if !s.CheckCSRF(token, header) || s.CheckCSRF(token, "") || s.CheckCSRF(token, header+"x") {
		t.Fatal("CSRF validation failed")
	}
	other, _ := randomToken()
	if s.CheckCSRF(other, header) {
		t.Fatal("CSRF not bound to session")
	}
}
