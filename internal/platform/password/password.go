// Package password hashes and verifies passwords with argon2id.
package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Parameters follow the OWASP minimum for argon2id. They are stored inside
// each hash, so raising them later keeps old hashes verifiable.
const (
	memoryKiB = 19 * 1024
	timeCost  = 2
	threads   = 1
	saltLen   = 16
	keyLen    = 32
)

// Hash returns an encoded argon2id hash in the standard PHC string format.
func Hash(plain string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(plain), salt, timeCost, memoryKiB, threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, timeCost, threads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// Valid reports whether encoded is a well-formed argon2id hash with sane
// parameters (a restored backup's hashes are checked with it). It does not
// say which password it is for.
func Valid(encoded string) bool {
	parts := strings.Split(encoded, "$")
	if len(encoded) > 512 || len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	var memory, time uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &par); err != nil {
		return false
	}
	// Bounds, so a crafted hash cannot make every login attempt slow.
	if memory < 8 || memory > 1<<20 || time < 1 || time > 16 || par < 1 || par > 16 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < 8 {
		return false
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	return err == nil && len(key) >= 16 && len(key) <= 64
}

// Verify reports whether plain matches the encoded hash.
func Verify(encoded, plain string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false
	}
	var version int
	var memory, time uint32
	var par uint8
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &time, &par); err != nil {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(plain), salt, time, memory, par, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}
