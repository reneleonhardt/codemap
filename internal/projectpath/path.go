// Package projectpath separates the analyzed project from its Codemap setup.
package projectpath

import (
	"path/filepath"
	"strings"
	"sync"
)

var configuredSetupRoot struct {
	sync.RWMutex
	path string
}

// SetupRoot returns the configured setup root or the analyzed project root.
func SetupRoot(projectRoot string) string {
	if root := ConfiguredSetupRoot(); root != "" {
		return filepath.Clean(root)
	}
	return projectRoot
}

// ConfiguredSetupRoot returns the invocation-wide override, if any.
func ConfiguredSetupRoot() string {
	configuredSetupRoot.RLock()
	defer configuredSetupRoot.RUnlock()
	return configuredSetupRoot.path
}

// SetSetupRoot configures the validated setup root for this process.
func SetSetupRoot(root string) {
	configuredSetupRoot.Lock()
	defer configuredSetupRoot.Unlock()
	root = strings.TrimSpace(root)
	if root == "" {
		configuredSetupRoot.path = ""
		return
	}
	configuredSetupRoot.path = filepath.Clean(root)
}

// ResetSetupRoot clears invocation-scoped setup state.
func ResetSetupRoot() {
	SetSetupRoot("")
}

// PrependSetupRootArgs preserves setup selection across Codemap subprocesses.
func PrependSetupRootArgs(args ...string) []string {
	root := ConfiguredSetupRoot()
	result := make([]string, 0, len(args)+2)
	if root != "" {
		result = append(result, "--setup-root", root)
	}
	return append(result, args...)
}

// CodemapDir returns the .codemap directory associated with a project.
func CodemapDir(projectRoot string) string {
	return filepath.Join(SetupRoot(projectRoot), ".codemap")
}
