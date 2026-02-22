//go:build !autostart

package bootstrap

func AutostartSupported() bool {
	return false
}

// ConfigureAutostart is disabled in default builds to reduce AV false-positive
// triggers from persistence-related OS integration behavior.
func ConfigureAutostart(binaryPath, cfgPath string) (bool, error) {
	_ = binaryPath
	_ = cfgPath
	return false, nil
}
