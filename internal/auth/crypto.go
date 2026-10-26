package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", errors.New("entropy unavailable")
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func tokenHash(token string) []byte { h := sha256.Sum256([]byte(token)); return h[:] }
func keyedHash(key []byte, value string) []byte {
	m := hmac.New(sha256.New, key)
	_, _ = m.Write([]byte(value))
	return m.Sum(nil)
}
func validPassword(password string) bool { return len(password) >= 12 && len(password) <= 256 }
func HashPassword(password string) (string, error) {
	if !validPassword(password) {
		return "", errors.New("password must contain 12 to 256 bytes")
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", errors.New("entropy unavailable")
	}
	hash := argon2.IDKey([]byte(password), salt, 3, 64*1024, 2, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=3,p=2$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash)), nil
}
func VerifyPassword(encoded, password string) bool {
	if len(password) > 256 {
		return false
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return false
	}
	numbers := make([]uint64, 3)
	for i, name := range []string{"m=", "t=", "p="} {
		if !strings.HasPrefix(params[i], name) {
			return false
		}
		v, err := strconv.ParseUint(strings.TrimPrefix(params[i], name), 10, 32)
		if err != nil {
			return false
		}
		numbers[i] = v
	}
	// Bounds are enforced before allocation; hashes are not a memory/CPU exhaustion vector.
	if numbers[0] != 65536 || numbers[1] != 3 || numbers[2] != 2 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) != 16 {
		return false
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(expected) != 32 {
		return false
	}
	actual := argon2.IDKey([]byte(password), salt, 3, 65536, 2, 32)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}
