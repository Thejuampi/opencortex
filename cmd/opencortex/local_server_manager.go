package main

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func isServerHealthy(baseURL string) bool {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = discoverServerURL()
	}
	client := &http.Client{Timeout: 900 * time.Millisecond}
	resp, err := client.Get(baseURL + "/healthz")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func waitForServerReady(baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if isServerHealthy(baseURL) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for server health at %s", baseURL)
}

func startLocalServerDetached(cfgPath string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	logPath := filepath.Join(opencortexDir(), "server.log")
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}

	args := []string{"server", "--config", cfgPath, "--open-browser=false"}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logf
	cmd.Stderr = logf
	cmd.Stdin = nil
	applyDetachedProcessAttrs(cmd)

	if err := cmd.Start(); err != nil {
		_ = logf.Close()
		return err
	}
	_ = cmd.Process.Release()
	_ = logf.Close()
	return nil
}

func stopLocalServer() (bool, error) {
	pid, _, err := readLockFile()
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(lockFilePath())
			_ = os.Remove(serverFilePath())
			return false, nil
		}
		return false, err
	}
	if pid <= 0 {
		_ = os.Remove(lockFilePath())
		_ = os.Remove(serverFilePath())
		return false, nil
	}

	proc, err := os.FindProcess(pid)
	if err == nil {
		_ = proc.Signal(os.Interrupt)
		for i := 0; i < 12; i++ {
			if !isProcessAlive(pid) {
				break
			}
			time.Sleep(250 * time.Millisecond)
		}
		if isProcessAlive(pid) {
			_ = proc.Kill()
		}
	}
	_ = os.Remove(lockFilePath())
	_ = os.Remove(serverFilePath())
	return true, nil
}

func ensureLocalServerReady(baseURL, cfgPath string) error {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = discoverServerURL()
	}
	if !isLocalURL(baseURL) {
		return nil
	}
	if isServerHealthy(baseURL) {
		st, _ := loadCLIState()
		st.LastKnownServerURL = baseURL
		st.LastStartError = ""
		_ = saveCLIState(st)
		return nil
	}

	isTTY := isCharDevice(os.Stdin) && isCharDevice(os.Stdout)
	allowed, known, err := resolveAutoStartConsent(isTTY, os.Getenv("OPENCORTEX_AUTO_START_CONSENT"))
	if err != nil {
		return err
	}
	if !known {
		return fmt.Errorf("local server is not running and auto-start consent is unknown; run `opencortex start` once or set OPENCORTEX_AUTO_START_CONSENT=1")
	}
	if !allowed {
		return fmt.Errorf("local server is not running and auto-start is disabled; run `opencortex start` or set OPENCORTEX_AUTO_START_CONSENT=1")
	}

	st, _ := loadCLIState()
	st.LastKnownServerURL = baseURL
	if err := startLocalServerDetached(cfgPath); err != nil {
		st.LastStartError = err.Error()
		_ = saveCLIState(st)
		return fmt.Errorf("failed to auto-start local server: %w", err)
	}
	if err := waitForServerReady(baseURL, 15*time.Second); err != nil {
		st.LastStartError = err.Error()
		_ = saveCLIState(st)
		return fmt.Errorf("server did not become healthy: %w", err)
	}
	st.LastStartError = ""
	_ = saveCLIState(st)
	return nil
}

func newAutoClientWithEnsure(baseURL, apiKey, cfgPath string) (*apiClient, error) {
	client := newAPIClient(baseURL, apiKey)
	if isLocalURL(client.baseURL) {
		if err := ensureLocalServerReady(client.baseURL, cfgPath); err != nil {
			return nil, err
		}
	}
	return newAutoClient(client.baseURL, apiKey), nil
}

func readLockFile() (int, string, error) {
	b, err := os.ReadFile(lockFilePath())
	if err != nil {
		return 0, "", err
	}
	parts := strings.SplitN(strings.TrimSpace(string(b)), " ", 2)
	if len(parts) == 0 {
		return 0, "", nil
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
	addr := ""
	if len(parts) > 1 {
		addr = strings.TrimSpace(parts[1])
	}
	return pid, addr, nil
}

func isCharDevice(f *os.File) bool {
	if f == nil {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
