//go:build autostart

package bootstrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	launchAgentLabel   = "dev.opencortex.server"
	systemdServiceName = "opencortex.service"
)

func AutostartSupported() bool {
	return true
}

func ConfigureAutostart(binaryPath, cfgPath string) (bool, error) {
	switch runtime.GOOS {
	case "darwin":
		return configureLaunchd(binaryPath, cfgPath)
	case "linux":
		return configureSystemd(binaryPath, cfgPath)
	case "windows":
		return configureWindowsTask(binaryPath, cfgPath)
	default:
		return false, nil
	}
}

func configureLaunchd(binaryPath, cfgPath string) (bool, error) {
	dir := filepath.Join(HomeDir(), "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	plistPath := filepath.Join(dir, launchAgentLabel+".plist")
	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
  <dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key>
    <array>
      <string>%s</string>
      <string>server</string>
      <string>--config</string>
      <string>%s</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
  </dict>
</plist>
`, launchAgentLabel, binaryPath, cfgPath)
	if err := os.WriteFile(plistPath, []byte(plist), 0o644); err != nil {
		return false, err
	}
	_, _ = exec.Command("launchctl", "unload", plistPath).CombinedOutput()
	_, _ = exec.Command("launchctl", "load", plistPath).CombinedOutput()
	return true, nil
}

func configureSystemd(binaryPath, cfgPath string) (bool, error) {
	dir := filepath.Join(HomeDir(), ".config", "systemd", "user")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return false, err
	}
	servicePath := filepath.Join(dir, systemdServiceName)
	unit := fmt.Sprintf(`[Unit]
Description=OpenCortex Server
After=network.target

[Service]
ExecStart=%s server --config %s
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, binaryPath, cfgPath)
	if err := os.WriteFile(servicePath, []byte(unit), 0o644); err != nil {
		return false, err
	}
	systemctlPath, err := exec.LookPath("systemctl")
	if err == nil {
		_, _ = exec.Command(systemctlPath, "--user", "daemon-reload").CombinedOutput()
		_, _ = exec.Command(systemctlPath, "--user", "enable", "--now", systemdServiceName).CombinedOutput()
	}
	return true, nil
}

func configureWindowsTask(binaryPath, cfgPath string) (bool, error) {
	taskName := "OpenCortexServer"
	args := fmt.Sprintf(`"%s" server --config "%s"`, binaryPath, cfgPath)
	cmd := exec.Command("schtasks", "/Create", "/F", "/SC", "ONLOGON", "/TN", taskName, "/TR", args)
	if out, err := cmd.CombinedOutput(); err != nil {
		return false, fmt.Errorf("configure task scheduler: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return true, nil
}
