package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"codemap/internal/projectpath"
)

func TestRunSkillInitUsesSetupRoot(t *testing.T) {
	projectRoot := t.TempDir()
	setupRoot := t.TempDir()
	projectpath.SetSetupRoot(setupRoot)
	t.Cleanup(projectpath.ResetSetupRoot)

	captureOutput(func() { runSkillInit(projectRoot) })
	want := filepath.Join(setupRoot, ".codemap", "skills", "my-skill.md")
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("setup-root skill template missing: %v", err)
	}
	projectPath := filepath.Join(projectRoot, ".codemap", "skills", "my-skill.md")
	if _, err := os.Stat(projectPath); !os.IsNotExist(err) {
		t.Fatalf("project-root skill template unexpectedly exists: %v", err)
	}
}
