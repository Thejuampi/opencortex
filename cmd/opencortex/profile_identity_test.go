package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func setConfigHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("APPDATA", root)
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
}

func TestNormalizeProfileName(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{in: "", want: "default"},
		{in: "Planner", want: "planner"},
		{in: "agent_01", want: "agent_01"},
		{in: "bad profile", wantErr: true},
		{in: "..", wantErr: true},
	}
	for _, tc := range cases {
		got, err := normalizeProfileName(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("expected error for %q", tc.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("normalize %q: %v", tc.in, err)
		}
		if got != tc.want {
			t.Fatalf("normalize %q => %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestAuthStoreLegacyMigrationToDefaultProfile(t *testing.T) {
	setConfigHome(t)

	path, err := authStorePath()
	if err != nil {
		t.Fatalf("authStorePath: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	legacy := map[string]any{
		"current": "http://localhost:8080",
		"accounts": map[string]any{
			"http://localhost:8080": map[string]any{
				"base_url": "http://localhost:8080",
				"api_key":  "k-legacy",
			},
		},
	}
	b, _ := json.Marshal(legacy)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatalf("write legacy auth: %v", err)
	}

	store, err := authStoreLoad()
	if err != nil {
		t.Fatalf("authStoreLoad: %v", err)
	}
	key := authAccountKey("http://localhost:8080", defaultAgentProfile)
	acc, ok := store.Accounts[key]
	if !ok {
		t.Fatalf("expected migrated key %s", key)
	}
	if acc.Profile != defaultAgentProfile {
		t.Fatalf("expected profile default, got %s", acc.Profile)
	}
	p, ok := store.CurrentByBaseURL["http://localhost:8080"]
	if !ok || p != defaultAgentProfile {
		t.Fatalf("expected current profile default, got %q", p)
	}
}

func TestAuthStoreGetTokenForProfilePrecedence(t *testing.T) {
	setConfigHome(t)

	base := "http://localhost:8080"
	store := authStore{
		CurrentByBaseURL: map[string]string{base: "planner"},
		Accounts: map[string]authAccount{
			authAccountKey(base, "default"): {BaseURL: base, Profile: "default", APIKey: "k-default"},
			authAccountKey(base, "planner"): {BaseURL: base, Profile: "planner", APIKey: "k-planner"},
			authAccountKey(base, "review"):  {BaseURL: base, Profile: "review", APIKey: "k-review"},
		},
	}
	if err := authStoreSave(store); err != nil {
		t.Fatalf("authStoreSave: %v", err)
	}

	if got, ok := authStoreGetTokenForProfile(base, "review"); !ok || got != "k-review" {
		t.Fatalf("expected review token, got %q ok=%v", got, ok)
	}
	if got, ok := authStoreGetTokenForProfile(base, "unknown"); !ok || got != "k-planner" {
		t.Fatalf("expected planner fallback token, got %q ok=%v", got, ok)
	}
	if got, ok := authStoreGetTokenForProfileMode(base, "unknown", false); ok || got != "" {
		t.Fatalf("expected strict profile lookup miss, got %q ok=%v", got, ok)
	}
	if got, ok := authStoreGetTokenForProfileMode(base, "review", false); !ok || got != "k-review" {
		t.Fatalf("expected strict review token, got %q ok=%v", got, ok)
	}

	store.CurrentByBaseURL[base] = "missing"
	if err := authStoreSave(store); err != nil {
		t.Fatalf("authStoreSave(2): %v", err)
	}
	if got, ok := authStoreGetTokenForProfile(base, "unknown"); !ok || got != "k-default" {
		t.Fatalf("expected default fallback token, got %q ok=%v", got, ok)
	}
	if got, ok := authStoreGetTokenForProfileMode(base, "unknown", false); ok || got != "" {
		t.Fatalf("expected strict miss after current profile is missing, got %q ok=%v", got, ok)
	}
}

func TestLocalFingerprintIncludesProfileAndBaseURL(t *testing.T) {
	t.Parallel()

	a := localFingerprint("http://localhost:8080", "planner")
	b := localFingerprint("http://localhost:8080", "planner")
	c := localFingerprint("http://localhost:8080", "reviewer")
	d := localFingerprint("http://localhost:8081", "planner")

	if a != b {
		t.Fatalf("expected stable fingerprint for same inputs")
	}
	if a == c {
		t.Fatalf("expected profile to affect fingerprint")
	}
	if a == d {
		t.Fatalf("expected base url to affect fingerprint")
	}
}
