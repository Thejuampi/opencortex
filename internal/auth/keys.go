package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"
)

const (
	KeyPrefixLive   = "amk_live_"
	KeyPrefixTest   = "amk_test_"
	KeyPrefixRemote = "amk_remote_"
	base62Alphabet  = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

func GenerateAPIKey(kind string) (string, string, error) {
	prefix := KeyPrefixLive
	switch kind {
	case "live":
		prefix = KeyPrefixLive
	case "test":
		prefix = KeyPrefixTest
	case "remote":
		prefix = KeyPrefixRemote
	default:
		return "", "", fmt.Errorf("invalid key kind: %s", kind)
	}

	suffix, err := randomBase62(32)
	if err != nil {
		return "", "", err
	}
	raw := prefix + suffix
	return raw, HashKey(raw), nil
}

func HashKey(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

func VerifyKey(raw, hash string) bool {
	computed := HashKey(raw)
	return subtle.ConstantTimeCompare([]byte(strings.ToLower(computed)), []byte(strings.ToLower(hash))) == 1
}

func DetectKeyKind(raw string) string {
	switch {
	case strings.HasPrefix(raw, KeyPrefixLive):
		return "live"
	case strings.HasPrefix(raw, KeyPrefixTest):
		return "test"
	case strings.HasPrefix(raw, KeyPrefixRemote):
		return "remote"
	default:
		return ""
	}
}

func randomBase62(n int) (string, error) {
	if n <= 0 {
		return "", nil
	}
	buf := make([]byte, n)
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("random: %w", err)
	}
	for i := range raw {
		buf[i] = base62Alphabet[int(raw[i])%len(base62Alphabet)]
	}
	return string(buf), nil
}
