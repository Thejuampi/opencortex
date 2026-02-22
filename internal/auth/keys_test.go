package auth

import "testing"

func TestGenerateAndVerifyKey(t *testing.T) {
	key, hash, err := GenerateAPIKey("live")
	if err != nil {
		t.Fatalf("GenerateAPIKey failed: %v", err)
	}
	if DetectKeyKind(key) != "live" {
		t.Fatalf("expected live key kind, got %s", DetectKeyKind(key))
	}
	if !VerifyKey(key, hash) {
		t.Fatal("VerifyKey returned false")
	}
	if VerifyKey(key+"x", hash) {
		t.Fatal("VerifyKey unexpectedly true")
	}
}
