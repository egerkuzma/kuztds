// Package security — admin authentication primitives: password hashing (argon2id),
// random tokens, sessions, and CSRF. Replaces the MD5 password and cookie auth
// with secure storage of the administrator password.
package security

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Default argon2id parameters (a reasonable balance for interactive login).
const (
	argonTime    = 3
	argonMemory  = 64 * 1024 // 64 MiB
	argonThreads = 2
	argonKeyLen  = 32
	argonSaltLen = 16
)

// HashPassword returns an encoded argon2id hash in the format
// $argon2id$v=19$m=...,t=...,p=...$salt$hash.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

// VerifyPassword compares a password against an encoded hash in constant time.
func VerifyPassword(password, encoded string) bool {
	p, salt, hash, err := decodeHash(encoded)
	if err != nil {
		return false
	}
	calc := argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, uint32(len(hash)))
	return subtle.ConstantTimeCompare(hash, calc) == 1
}

type params struct {
	memory  uint32
	time    uint32
	threads uint8
}

// ErrBadHash reports an encoded hash this package refuses to use. Callers match
// it with errors.Is; the wrapped text says which part was wrong, and never
// repeats the hash itself — it ends up in logs, which outlive passwords.
var ErrBadHash = errors.New("security: invalid hash format")

// Bounds on the cost parameters read out of an encoded hash.
//
// They are not tuning knobs, they are a guard. argon2.IDKey panics outright on
// t=0 ("number of rounds too small") and p=0 ("parallelism degree too low"), and
// allocates m KiB — an m of 4194304 asks for four gigabytes on a single login
// attempt. All three come from the hash string, i.e. from an environment
// variable or a file: a typo while copying, not an attack, and the process
// falls over the same either way. The upper limits sit far above anything
// HashPassword produces, so a legitimate hash never meets them.
const (
	minMemory  = 8 * 1024        // 8 MiB
	maxMemory  = 1 * 1024 * 1024 // 1 GiB
	minTime    = 1
	maxTime    = 16
	minThreads = 1
)

// ValidateHash reports whether an encoded hash can be used for verification.
// Check it wherever a hash enters the process, so that a bad one is refused at
// the door rather than at the next login.
func ValidateHash(encoded string) error {
	_, _, _, err := decodeHash(encoded)
	return err
}

func decodeHash(encoded string) (params, []byte, []byte, error) {
	fail := func(format string, args ...any) (params, []byte, []byte, error) {
		return params{}, nil, nil, fmt.Errorf("%w: %s", ErrBadHash, fmt.Sprintf(format, args...))
	}
	parts := strings.Split(encoded, "$")
	// ["", "argon2id", "v=19", "m=..,t=..,p=..", salt, hash]
	if len(parts) != 6 || parts[1] != "argon2id" {
		return fail("expected 6 $-separated argon2id fields, got %d", len(parts))
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return fail("unsupported version field %q, want v=%d", parts[2], argon2.Version)
	}
	var p params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return fail("unparsable cost parameters %q", parts[3])
	}
	if p.memory < minMemory || p.memory > maxMemory {
		return fail("m=%d outside %d..%d", p.memory, minMemory, maxMemory)
	}
	if p.time < minTime || p.time > maxTime {
		return fail("t=%d outside %d..%d", p.time, minTime, maxTime)
	}
	if p.threads < minThreads {
		return fail("p=%d below %d", p.threads, minThreads)
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return fail("salt is not raw-std base64")
	}
	hash, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return fail("hash is not raw-std base64")
	}
	if len(hash) == 0 {
		return fail("hash is empty")
	}
	return p, salt, hash, nil
}
