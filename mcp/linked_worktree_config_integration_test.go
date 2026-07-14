package codemapmcp

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"codemap/internal/projectpath"
	"codemap/scanner"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPHandlersUseEachLinkedPrimaryConfig(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	projectpath.ResetSetupRoot()
	t.Cleanup(projectpath.ResetSetupRoot)

	type fixture struct {
		linked   string
		allowed  string
		shared   string
		wrong    string
		excluded string
	}

	writeFiles := func(root string, files map[string]string) {
		t.Helper()
		for path, content := range files {
			full := filepath.Join(root, path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	makeFixture := func(name, configJSON string, files map[string]string, allowed, shared, wrong, excluded string) fixture {
		t.Helper()
		primary := makeMCPGitRepo(t, "main")
		writeFiles(primary, files)
		runGitMCPTestCmd(t, primary, "add", ".")
		runGitMCPTestCmd(t, primary, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "fixture")

		linked := filepath.Join(t.TempDir(), "linked")
		runGitMCPTestCmd(t, primary, "worktree", "add", "-b", "linked-"+name, linked, "main")

		configDir := filepath.Join(primary, ".codemap")
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(configDir, "config.json"), []byte(configJSON), 0o644); err != nil {
			t.Fatal(err)
		}

		for _, path := range []string{allowed, wrong, excluded} {
			file, err := os.OpenFile(filepath.Join(linked, path), os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n// changed\n"); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		}

		return fixture{linked: linked, allowed: allowed, shared: shared, wrong: wrong, excluded: excluded}
	}

	fixtures := []fixture{
		makeFixture(
			"a",
			`{"only":["go"],"exclude":["excluded-a"]}`,
			map[string]string{
				"go.mod":                        "module example.com/a\n\ngo 1.22\n",
				"allowed_policy_a.go":           "package main\n\nimport _ \"example.com/a/shared\"\n",
				"shared/shared_a.go":            "package shared\n",
				"wrong_policy_a.ts":             "import './shared/shared_a'\n",
				"excluded-a/hidden_policy_a.go": "package hidden\n",
			},
			"allowed_policy_a.go",
			"shared_a.go",
			"wrong_policy_a.ts",
			"excluded-a/hidden_policy_a.go",
		),
		makeFixture(
			"b",
			`{"only":["ts"],"exclude":["excluded-b"]}`,
			map[string]string{
				"allowed_policy_b.ts":           "import './shared/shared_b'\n",
				"shared/shared_b.ts":            "export const sharedB = 1\n",
				"wrong_policy_b.go":             "package main\n",
				"excluded-b/hidden_policy_b.ts": "export const hiddenB = 1\n",
			},
			"allowed_policy_b.ts",
			"shared_b.ts",
			"wrong_policy_b.go",
			"excluded-b/hidden_policy_b.ts",
		),
	}

	assertResult := func(name string, result *mcp.CallToolResult, err error) string {
		t.Helper()
		if err != nil {
			t.Fatalf("%s transport error: %v", name, err)
		}
		if result.IsError {
			t.Fatalf("%s result error:\n%s", name, resultText(t, result))
		}
		return resultText(t, result)
	}
	assertFiltered := func(name, output string, fx fixture, wantShared bool) {
		t.Helper()
		allowed := fx.allowed
		shared := fx.shared
		wrong := fx.wrong
		excluded := fx.excluded
		if name == "structure" || name == "diff" {
			allowed = strings.TrimSuffix(filepath.Base(allowed), filepath.Ext(allowed))
			shared = strings.TrimSuffix(filepath.Base(shared), filepath.Ext(shared))
			wrong = strings.TrimSuffix(filepath.Base(wrong), filepath.Ext(wrong))
			excluded = strings.Split(filepath.ToSlash(excluded), "/")[0]
		}
		if !strings.Contains(output, allowed) {
			t.Fatalf("%s missing allowed file %q:\n%s", name, allowed, output)
		}
		if wantShared && !strings.Contains(output, shared) {
			t.Fatalf("%s missing shared file %q:\n%s", name, shared, output)
		}
		for _, unwanted := range []string{wrong, excluded} {
			if strings.Contains(output, unwanted) {
				t.Fatalf("%s contains configured-out file %q:\n%s", name, unwanted, output)
			}
		}
	}

	for _, fx := range fixtures {
		structure, _, err := handleGetStructure(context.Background(), nil, PathInput{Path: fx.linked})
		assertFiltered("structure", assertResult("structure", structure, err), fx, true)

		diff, _, err := handleGetDiff(context.Background(), nil, DiffInput{Path: fx.linked, Ref: "main"})
		assertFiltered("diff", assertResult("diff", diff, err), fx, false)

		found, _, err := handleFindFile(context.Background(), nil, FindInput{Path: fx.linked, Pattern: "policy_"})
		assertFiltered("find", assertResult("find", found, err), fx, false)
	}

	if scanner.NewAstGrepAnalyzer().Available() {
		for _, fx := range fixtures {
			deps, _, err := handleGetDependencies(context.Background(), nil, PathInput{Path: fx.linked})
			output := assertResult("dependencies", deps, err)
			if strings.Contains(output, "0 files") {
				t.Fatalf("dependencies omitted all configured-in files:\n%s", output)
			}
			for _, unwanted := range []string{fx.wrong, fx.excluded} {
				if strings.Contains(output, unwanted) {
					t.Fatalf("dependencies contains configured-out file %q:\n%s", unwanted, output)
				}
			}
		}
	} else {
		t.Log("ast-grep not available; dependency subcheck skipped")
	}
}
