package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestGlobalRootOptionsEndToEnd(t *testing.T) {
	root := t.TempDir()
	projectRoot := filepath.Join(root, "worktree")
	setupRoot := filepath.Join(root, "original")
	for _, repo := range []string{projectRoot, setupRoot} {
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	projectNested := filepath.Join(projectRoot, "pkg", "feature")
	setupNested := filepath.Join(setupRoot, "cmd")
	if err := os.MkdirAll(projectNested, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(setupNested, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestSkill(t, setupRoot, "setup-only")

	t.Run("relative setup root reuses original storage", func(t *testing.T) {
		relSetup, err := filepath.Rel(projectRoot, setupNested)
		if err != nil {
			t.Fatal(err)
		}
		out, err := runRootOptionsBinary(projectNested,
			"-C", ".",
			"--setup-root", relSetup,
			"skill", "list",
		)
		if err != nil {
			t.Fatalf("codemap failed: %v\n%s", err, out)
		}
		if !strings.Contains(out, "setup-only") {
			t.Fatalf("setup-root skill missing from output:\n%s", out)
		}
	})

	t.Run("inherited setup root environment is ignored", func(t *testing.T) {
		hostileRoot := filepath.Join(root, "hostile")
		if err := os.MkdirAll(filepath.Join(hostileRoot, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeTestSkill(t, hostileRoot, "hostile-only")

		command := exec.Command(codemapTestBinaryPath,
			"--project-root", projectNested,
			"skill", "list",
		)
		command.Env = append(os.Environ(), "CODEMAP_SETUP_ROOT="+hostileRoot)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("codemap failed: %v\n%s", err, out)
		}
		if strings.Contains(string(out), "hostile-only") {
			t.Fatalf("inherited environment redirected setup storage:\n%s", out)
		}
	})

	t.Run("symlinked codemap storage is rejected", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("symlinks may require elevated privileges")
		}
		unsafeRoot := filepath.Join(root, "unsafe")
		if err := os.MkdirAll(filepath.Join(unsafeRoot, ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(t.TempDir(), filepath.Join(unsafeRoot, ".codemap")); err != nil {
			t.Fatal(err)
		}

		out, err := runRootOptionsBinary(projectNested,
			"--setup-root", unsafeRoot,
			"skill", "list",
		)
		if err == nil {
			t.Fatalf("codemap unexpectedly accepted symlinked storage:\n%s", out)
		}
		if !strings.Contains(out, "unsafe Codemap storage") {
			t.Fatalf("unexpected rejection output:\n%s", out)
		}
	})
}

func writeTestSkill(t *testing.T, root, name string) {
	t.Helper()
	dir := filepath.Join(root, ".codemap", "skills")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: root option fixture\n---\n\n# Fixture\n"
	if err := os.WriteFile(filepath.Join(dir, name+".md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runRootOptionsBinary(dir string, args ...string) (string, error) {
	command := exec.Command(codemapTestBinaryPath, args...)
	command.Dir = dir
	out, err := command.CombinedOutput()
	return string(out), err
}
