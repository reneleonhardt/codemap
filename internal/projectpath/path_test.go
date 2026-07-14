package projectpath

import (
	"path/filepath"
	"testing"
)

func TestSetupRoot(t *testing.T) {
	projectRoot := filepath.Join(t.TempDir(), "project")
	setupRoot := filepath.Join(t.TempDir(), "setup")

	t.Run("defaults to project root", func(t *testing.T) {
		ResetSetupRoot()
		t.Cleanup(ResetSetupRoot)
		t.Setenv("CODEMAP_SETUP_ROOT", setupRoot)
		if got := SetupRoot(projectRoot); got != projectRoot {
			t.Fatalf("SetupRoot() = %q, want %q", got, projectRoot)
		}
		if got := ConfiguredSetupRoot(); got != "" {
			t.Fatalf("ConfiguredSetupRoot() = %q, want empty despite inherited environment", got)
		}
	})

	t.Run("uses configured setup root", func(t *testing.T) {
		SetSetupRoot(setupRoot)
		t.Cleanup(ResetSetupRoot)
		if got := SetupRoot(projectRoot); got != setupRoot {
			t.Fatalf("SetupRoot() = %q, want %q", got, setupRoot)
		}
		want := filepath.Join(setupRoot, ".codemap")
		if got := CodemapDir(projectRoot); got != want {
			t.Fatalf("CodemapDir() = %q, want %q", got, want)
		}
	})

	t.Run("clears configured setup root", func(t *testing.T) {
		SetSetupRoot(setupRoot)
		ResetSetupRoot()
		if got := SetupRoot(projectRoot); got != projectRoot {
			t.Fatalf("SetupRoot() = %q, want %q", got, projectRoot)
		}
	})
}

func TestPrependSetupRootArgs(t *testing.T) {
	SetSetupRoot("/setup/root")
	t.Cleanup(ResetSetupRoot)

	got := PrependSetupRootArgs("watch", "start", "/project")
	want := []string{"--setup-root", "/setup/root", "watch", "start", "/project"}
	if len(got) != len(want) {
		t.Fatalf("PrependSetupRootArgs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("PrependSetupRootArgs() = %#v, want %#v", got, want)
		}
	}
}
