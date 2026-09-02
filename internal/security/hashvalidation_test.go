package security

import (
	"errors"
	"strings"
	"testing"
)

// The salt and hash below are well-formed base64; only the cost parameters
// change, so each case fails for the reason it is named after.
func encodedWith(params string) string {
	return "$argon2id$v=19$" + params +
		"$c2FsdHNhbHRzYWx0c2Fs$aGFzaGhhc2hoYXNoaGFzaGhhc2hoYXNoaGFzaGhhcw"
}

// TestVerifyPasswordSurvivesDegenerateParams is the reason ValidateHash exists.
//
// decodeHash parsed m, t and p straight out of the encoded hash and handed them
// to argon2.IDKey without looking. IDKey does not tolerate the degenerate
// values: t=0 panics with "number of rounds too small" and p=0 with
// "parallelism degree too low", and VerifyPassword recovers from neither. The
// hash comes from an environment variable or a file, so this is a typo while
// copying, and it turned every login attempt — unauthenticated, before any
// credential check — into a panic.
//
// m is the other half: it is allocated, so m=4194304 asks for four gigabytes on
// a single login attempt.
func TestVerifyPasswordSurvivesDegenerateParams(t *testing.T) {
	for _, params := range []string{
		"m=65536,t=3,p=0",    // panicked: parallelism degree too low
		"m=65536,t=0,p=2",    // panicked: number of rounds too small
		"m=0,t=3,p=2",        // no memory
		"m=4194304,t=3,p=2",  // four gigabytes on one login attempt
		"m=65536,t=999,p=2",  // absurd cost
		"m=nonsense,t=3,p=2", // unparsable
	} {
		t.Run(params, func(t *testing.T) {
			enc := encodedWith(params)
			if err := ValidateHash(enc); !errors.Is(err, ErrBadHash) {
				t.Fatalf("ValidateHash = %v, want ErrBadHash", err)
			}
			if VerifyPassword("whatever", enc) {
				t.Fatal("a hash we refuse to validate must never verify")
			}
		})
	}
}

// A hash this package produced must always pass its own gate. The check is
// cheap and it is the one that catches a change to the cost constants or to the
// encoding that leaves the two halves disagreeing.
func TestHashPasswordRoundTripsThroughValidation(t *testing.T) {
	enc, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateHash(enc); err != nil {
		t.Fatalf("our own hash failed validation: %v", err)
	}
	if !VerifyPassword("correct horse", enc) {
		t.Fatal("our own hash failed verification")
	}
}

// The error says which field was wrong and never repeats the hash: it travels
// to a log collector that outlives the password, and the login form is where
// people put their password in the wrong box.
func TestBadHashErrorNamesTheDefectNotTheSecret(t *testing.T) {
	enc := encodedWith("m=65536,t=0,p=2")
	err := ValidateHash(enc)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "t=0") {
		t.Errorf("error %q does not say what was wrong", err)
	}
	if strings.Contains(err.Error(), "aGFzaGhhc2") {
		t.Errorf("error %q leaks the hash", err)
	}
}
