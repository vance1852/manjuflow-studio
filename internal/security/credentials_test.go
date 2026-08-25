package security

import (
	"strings"
	"testing"

	"github.com/vance1852/manjuflow-studio/internal/apperr"
)

func TestPasswordPolicyRejectsWeakCredentials(t *testing.T) {
	for _, candidate := range []string{"", "short1", "alllettersonly", "1234567890", strings.Repeat("a1", 200)} {
		if err := ValidatePassword(candidate); !apperr.IsCode(err, apperr.CodeInvalidArgument) {
			t.Fatalf("password %q reported %v", candidate, apperr.CodeOf(err))
		}
	}
	if err := ValidatePassword("director-pass-2026"); err != nil {
		t.Fatalf("a compliant password was refused: %v", err)
	}
}

func TestHashingProducesASaltedVerifiableDigest(t *testing.T) {
	const password = "director-pass-2026"
	first, err := HashPassword(password)
	if err != nil {
		t.Fatalf("hashing failed: %v", err)
	}
	second, err := HashPassword(password)
	if err != nil {
		t.Fatalf("second hashing failed: %v", err)
	}
	if first == second {
		t.Fatal("two hashes of the same password are identical, the salt is missing")
	}
	if !strings.HasPrefix(first, "pbkdf2-sha256$") {
		t.Fatalf("digest does not record its scheme: %s", first)
	}
	if strings.Contains(first, password) {
		t.Fatal("the digest embeds the plaintext")
	}
	if err := VerifyPassword(first, password); err != nil {
		t.Fatalf("the digest does not verify its own password: %v", err)
	}
	if err := VerifyPassword(second, password); err != nil {
		t.Fatalf("the second digest does not verify: %v", err)
	}
	if err := VerifyPassword(first, password+"x"); !apperr.IsCode(err, apperr.CodeUnauthenticated) {
		t.Fatalf("a wrong password reported %v", apperr.CodeOf(err))
	}
}

func TestVerifyRejectsMalformedStoredDigests(t *testing.T) {
	cases := []string{
		"",
		"plain-text",
		"pbkdf2-sha256$notanumber$c2FsdA$aGFzaA",
		"bcrypt$10$c2FsdA$aGFzaA",
		"pbkdf2-sha256$1000$!!!$aGFzaA",
	}
	for _, stored := range cases {
		if err := VerifyPassword(stored, "director-pass-2026"); err == nil {
			t.Fatalf("stored digest %q was accepted", stored)
		}
	}
}

func TestSessionTokensAreUniqueAndOnlyStoredAsDigests(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 32; i++ {
		token, err := NewSessionToken()
		if err != nil {
			t.Fatalf("token generation failed: %v", err)
		}
		if len(token) < 40 {
			t.Fatalf("token %q is too short to be unguessable", token)
		}
		if seen[token] {
			t.Fatalf("token %q was generated twice", token)
		}
		seen[token] = true

		digest := HashToken(token)
		if len(digest) != 64 {
			t.Fatalf("digest %q is not a sha256 hex string", digest)
		}
		if strings.Contains(digest, token) {
			t.Fatal("the digest embeds the token")
		}
		if HashToken(token) != digest {
			t.Fatal("the digest is not stable")
		}
	}
}

func TestFingerprintIsStableAndOrderSensitive(t *testing.T) {
	first := Fingerprint("POST", "/v1/shots/render", "17")
	if first != Fingerprint("POST", "/v1/shots/render", "17") {
		t.Fatal("the fingerprint is not stable")
	}
	if first == Fingerprint("POST", "/v1/shots/render", "18") {
		t.Fatal("different payloads share a fingerprint")
	}
	if first == Fingerprint("POST", "17", "/v1/shots/render") {
		t.Fatal("the fingerprint ignores field order")
	}
	if Fingerprint("ab", "c") == Fingerprint("a", "bc") {
		t.Fatal("the fingerprint does not separate adjacent fields")
	}
	if len(Fingerprint()) != 64 {
		t.Fatalf("an empty fingerprint is %d characters", len(Fingerprint()))
	}
}
