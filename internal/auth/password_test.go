package auth_test

import (
	"strings"
	"testing"

	"dockbrr/internal/auth"
)

func TestHashVerifyRoundTrip(t *testing.T) {
	h, err := auth.HashPassword("hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$") {
		t.Fatalf("hash %q missing argon2id prefix", h)
	}
	ok, err := auth.VerifyPassword(h, "hunter2")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("correct password did not verify")
	}
}

func TestVerifyWrongPassword(t *testing.T) {
	h, _ := auth.HashPassword("hunter2")
	ok, err := auth.VerifyPassword(h, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password verified")
	}
}

func TestHashIsSaltedAndUnique(t *testing.T) {
	a, _ := auth.HashPassword("same")
	b, _ := auth.HashPassword("same")
	if a == b {
		t.Fatal("two hashes of the same password are identical (missing random salt)")
	}
}

func TestVerifyRejectsMalformed(t *testing.T) {
	for _, bad := range []string{"", "plaintext", "$argon2id$v=19$bad", "$bcrypt$x$y$z$w$v"} {
		if _, err := auth.VerifyPassword(bad, "x"); err == nil {
			t.Fatalf("malformed hash %q verified without error", bad)
		}
	}
}
