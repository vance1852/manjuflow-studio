// Package security implements password hashing and opaque session tokens. It
// depends only on the standard library so the service builds with CGO disabled.
package security

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

const (
	// DefaultIterations is the PBKDF2 round count for new passwords.
	DefaultIterations = 24000
	saltLength        = 16
	keyLength         = 32
	hashScheme        = "pbkdf2-sha256"
	// MinPasswordLength is the shortest accepted password.
	MinPasswordLength = 10
)

// HashPassword derives a storable digest of the plaintext password.
func HashPassword(plaintext string) (string, error) {
	if err := ValidatePassword(plaintext); err != nil {
		return "", err
	}
	salt := make([]byte, saltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "cannot read salt entropy")
	}
	derived := pbkdf2SHA256([]byte(plaintext), salt, DefaultIterations, keyLength)
	return strings.Join([]string{
		hashScheme,
		strconv.Itoa(DefaultIterations),
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derived),
	}, "$"), nil
}

// ValidatePassword enforces the minimum credential policy.
func ValidatePassword(plaintext string) error {
	if len(plaintext) < MinPasswordLength {
		return apperr.New(apperr.CodeInvalidArgument, "password must be at least %d characters", MinPasswordLength).
			With("field", "password")
	}
	if len(plaintext) > 256 {
		return apperr.New(apperr.CodeInvalidArgument, "password must not exceed 256 characters").With("field", "password")
	}
	var hasLetter, hasDigit bool
	for _, r := range plaintext {
		switch {
		case r >= '0' && r <= '9':
			hasDigit = true
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			hasLetter = true
		}
	}
	if !hasLetter || !hasDigit {
		return apperr.New(apperr.CodeInvalidArgument, "password must mix letters and digits").With("field", "password")
	}
	return nil
}

// VerifyPassword compares a plaintext candidate against a stored digest in
// constant time.
func VerifyPassword(encoded, candidate string) error {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != hashScheme {
		return apperr.New(apperr.CodeInternal, "stored credential uses an unsupported scheme")
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations <= 0 {
		return apperr.New(apperr.CodeInternal, "stored credential has an invalid round count")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "stored credential has an invalid salt")
	}
	expected, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return apperr.Wrap(err, apperr.CodeInternal, "stored credential has an invalid digest")
	}
	derived := pbkdf2SHA256([]byte(candidate), salt, iterations, len(expected))
	if subtle.ConstantTimeCompare(derived, expected) != 1 {
		return apperr.New(apperr.CodeUnauthenticated, "credentials do not match")
	}
	return nil
}

// NewSessionToken returns a fresh opaque bearer token. The caller stores only
// its digest.
func NewSessionToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", apperr.Wrap(err, apperr.CodeInternal, "cannot read token entropy")
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// HashToken derives the stored digest of a bearer token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Fingerprint builds a stable digest of an ordered field list. It backs
// idempotency payload comparison and render artifact identifiers.
func Fingerprint(parts ...string) string {
	hasher := sha256.New()
	for _, part := range parts {
		_, _ = fmt.Fprintf(hasher, "%d:%s|", len(part), part)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func pbkdf2SHA256(password, salt []byte, iterations, length int) []byte {
	mac := hmac.New(sha256.New, password)
	hashLen := mac.Size()
	blocks := (length + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	block := make([]byte, hashLen)
	for index := 1; index <= blocks; index++ {
		mac.Reset()
		mac.Write(salt)
		binary.BigEndian.PutUint32(buf, uint32(index))
		mac.Write(buf)
		current := mac.Sum(nil)
		copy(block, current)
		for round := 2; round <= iterations; round++ {
			mac.Reset()
			mac.Write(current)
			current = mac.Sum(current[:0])
			for i := range block {
				block[i] ^= current[i]
			}
		}
		out = append(out, block...)
	}
	return out[:length]
}
