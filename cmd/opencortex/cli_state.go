package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const cliStateVersion = 1

type cliState struct {
	Version              int        `json:"version"`
	AutoStartLocalServer *bool      `json:"auto_start_local_server,omitempty"`
	LastPromptedAt       *time.Time `json:"last_prompted_at,omitempty"`
	LastStartError       string     `json:"last_start_error,omitempty"`
	LastKnownServerURL   string     `json:"last_known_server_url,omitempty"`
}

func cliStatePath() string {
	return filepath.Join(opencortexDir(), "cli-state.json")
}

func loadCLIState() (cliState, error) {
	b, err := os.ReadFile(cliStatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cliState{Version: cliStateVersion}, nil
		}
		return cliState{}, err
	}
	var st cliState
	if err := json.Unmarshal(b, &st); err != nil {
		return cliState{}, err
	}
	if st.Version == 0 {
		st.Version = cliStateVersion
	}
	return st, nil
}

func saveCLIState(st cliState) error {
	st.Version = cliStateVersion
	if err := os.MkdirAll(filepath.Dir(cliStatePath()), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(cliStatePath(), b, 0o600)
}

func parseConsentOverride(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "y":
		return true, true
	case "0", "false", "no", "n":
		return false, true
	default:
		return false, false
	}
}

func resolveAutoStartConsent(isTTY bool, envOverride string) (allowed bool, known bool, err error) {
	st, err := loadCLIState()
	if err != nil {
		return false, false, err
	}

	if v, ok := parseConsentOverride(envOverride); ok {
		now := time.Now().UTC()
		st.AutoStartLocalServer = &v
		st.LastPromptedAt = &now
		if saveErr := saveCLIState(st); saveErr != nil {
			return false, false, saveErr
		}
		return v, true, nil
	}

	if st.AutoStartLocalServer != nil {
		return *st.AutoStartLocalServer, true, nil
	}

	if !isTTY {
		return false, false, nil
	}

	fmt.Print("Local server is not running. Start it automatically now and remember this choice? [Y/n] ")
	line, readErr := bufio.NewReader(os.Stdin).ReadString('\n')
	if readErr != nil {
		return false, false, readErr
	}
	choice := strings.TrimSpace(strings.ToLower(line))
	v := !(choice == "n" || choice == "no")

	now := time.Now().UTC()
	st.AutoStartLocalServer = &v
	st.LastPromptedAt = &now
	if saveErr := saveCLIState(st); saveErr != nil {
		return false, false, saveErr
	}
	return v, true, nil
}
