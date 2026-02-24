package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIStateRoundTrip(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)

	v := true
	st := cliState{
		AutoStartLocalServer: &v,
		LastStartError:       "boom",
		LastKnownServerURL:   "http://127.0.0.1:8080",
	}
	if err := saveCLIState(st); err != nil {
		t.Fatalf("saveCLIState: %v", err)
	}
	got, err := loadCLIState()
	if err != nil {
		t.Fatalf("loadCLIState: %v", err)
	}
	if got.AutoStartLocalServer == nil || !*got.AutoStartLocalServer {
		t.Fatalf("expected auto_start_local_server=true")
	}
	if got.LastStartError != "boom" {
		t.Fatalf("unexpected LastStartError %q", got.LastStartError)
	}
	if got.LastKnownServerURL != "http://127.0.0.1:8080" {
		t.Fatalf("unexpected LastKnownServerURL %q", got.LastKnownServerURL)
	}
	if _, err := os.Stat(filepath.Join(home, ".opencortex", "cli-state.json")); err != nil {
		t.Fatalf("expected cli-state file: %v", err)
	}
}

func TestResolveAutoStartConsentFromEnv(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)

	allowed, known, err := resolveAutoStartConsent(false, "1")
	if err != nil {
		t.Fatalf("resolveAutoStartConsent: %v", err)
	}
	if !known || !allowed {
		t.Fatalf("expected known=true allowed=true, got known=%v allowed=%v", known, allowed)
	}
	st, err := loadCLIState()
	if err != nil {
		t.Fatalf("loadCLIState: %v", err)
	}
	if st.AutoStartLocalServer == nil || !*st.AutoStartLocalServer {
		t.Fatalf("expected persisted consent true")
	}
}

func TestResolveAutoStartConsentUnknownNonTTY(t *testing.T) {
	home := t.TempDir()
	setHomeEnv(t, home)

	allowed, known, err := resolveAutoStartConsent(false, "")
	if err != nil {
		t.Fatalf("resolveAutoStartConsent: %v", err)
	}
	if known || allowed {
		t.Fatalf("expected unknown consent in non-tty mode")
	}
}

func setHomeEnv(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOMEDRIVE", "")
	t.Setenv("HOMEPATH", "")
}
