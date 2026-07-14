package handoff

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"codemap/limits"
	"codemap/watch"
)

func TestBuildUsesConfiguredFileCountForPrefixAndBudget(t *testing.T) {
	root := t.TempDir()
	runCmd(t, root, "git", "init")

	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "notes.md"), []byte("not configured source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCmd(t, root, "git", "add", ".")
	runCmd(t, root, "git", "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")

	if err := os.MkdirAll(filepath.Join(root, ".codemap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codemap", "config.json"), []byte(`{"only":["go"]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 30; i++ {
		name := filepath.Join(root, fmt.Sprintf("file%02d.go", i))
		if err := os.WriteFile(name, []byte("package example\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	artifact, err := Build(root, BuildOptions{
		BaseRef: "HEAD",
		State: &watch.State{
			FileCount: limits.LargeRepoFileCount + 1,
			Importers: map[string][]string{"unused.go": {}},
		},
	})
	if err != nil {
		t.Fatalf("Build failed: %v", err)
	}

	if artifact.Prefix.FileCount != 30 {
		t.Fatalf("configured file count = %d, want 30", artifact.Prefix.FileCount)
	}
	if len(artifact.Delta.Changed) != 30 {
		t.Fatalf("configured small-repo budget kept %d changed files, want 30", len(artifact.Delta.Changed))
	}
}

func TestDependencyContextUsesConfiguredCountInsteadOfWatchCount(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codemap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codemap", "config.json"), []byte(`{"only":["go"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example\n\ngo 1.25\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "lib", "lib.go"), []byte("package lib\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "cmd", "main.go"), []byte("package main\n\nimport _ \"example/lib\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	importers, _ := dependencyContextForFile(root, &watch.State{FileCount: limits.LargeRepoFileCount + 1}, "lib/lib.go")
	if len(importers) != 1 || importers[0] != "cmd/main.go" {
		t.Fatalf("configured dependency importers = %v, want [cmd/main.go]", importers)
	}
}
