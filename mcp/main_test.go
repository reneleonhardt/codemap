package codemapmcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"codemap/handoff"
	"codemap/internal/buildinfo"
	"codemap/internal/projectpath"
	"codemap/scanner"
	"codemap/watch"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTextResultAndErrorResult(t *testing.T) {
	ok := textResult("ready")
	if ok.IsError {
		t.Fatal("textResult should not mark errors")
	}
	if got := resultText(t, ok); got != "ready" {
		t.Fatalf("textResult text = %q, want ready", got)
	}

	bad := errorResult("boom")
	if !bad.IsError {
		t.Fatal("errorResult should mark IsError")
	}
	if got := resultText(t, bad); got != "boom" {
		t.Fatalf("errorResult text = %q, want boom", got)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[31mred\x1b[0m plain"
	if got := stripANSI(in); got != "red plain" {
		t.Fatalf("stripANSI(%q) = %q", in, got)
	}
}

func TestGetProjectStatsAndHandleListProjects(t *testing.T) {
	parent := t.TempDir()
	alpha := filepath.Join(parent, "alpha")
	beta := filepath.Join(parent, "beta")
	if err := os.MkdirAll(filepath.Join(alpha, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(beta, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(alpha, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(beta, "app.py"), []byte("print('ok')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	stats := getProjectStats(alpha)
	if !strings.Contains(stats, "1 files") || !strings.Contains(stats, "Go") || !strings.Contains(stats, "[git]") {
		t.Fatalf("unexpected project stats: %s", stats)
	}

	res, _, err := handleListProjects(context.Background(), nil, ListProjectsInput{Path: parent, Pattern: "alp"})
	if err != nil {
		t.Fatalf("handleListProjects error: %v", err)
	}
	out := resultText(t, res)
	if !strings.Contains(out, "Projects matching 'alp'") || !strings.Contains(out, "alpha/") || strings.Contains(out, "beta/") {
		t.Fatalf("unexpected list_projects output:\n%s", out)
	}
}

func TestHandleFindFileAndStatus(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "helper.go"), []byte("package nested\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	findRes, _, err := handleFindFile(context.Background(), nil, FindInput{Path: root, Pattern: "main"})
	if err != nil {
		t.Fatalf("handleFindFile error: %v", err)
	}
	findOut := resultText(t, findRes)
	if !strings.Contains(findOut, "Found 1 files") || !strings.Contains(findOut, "main.go") {
		t.Fatalf("unexpected find output:\n%s", findOut)
	}

	missingRes, _, err := handleFindFile(context.Background(), nil, FindInput{Path: root, Pattern: "absent"})
	if err != nil {
		t.Fatalf("handleFindFile missing error: %v", err)
	}
	if !strings.Contains(resultText(t, missingRes), "No files found matching 'absent'") {
		t.Fatalf("unexpected missing-file output:\n%s", resultText(t, missingRes))
	}

	watchersMu.Lock()
	watchers = map[string]*watch.Daemon{"/tmp/demo": nil}
	watchersMu.Unlock()
	t.Cleanup(func() {
		watchersMu.Lock()
		watchers = make(map[string]*watch.Daemon)
		watchersMu.Unlock()
	})

	statusRes, _, err := handleStatus(context.Background(), nil, StatusInput{})
	if err != nil {
		t.Fatalf("handleStatus error: %v", err)
	}
	statusOut := resultText(t, statusRes)
	if !strings.Contains(statusOut, "codemap MCP server") || !strings.Contains(statusOut, "Active watchers: 1 active: /tmp/demo") {
		t.Fatalf("unexpected status output:\n%s", statusOut)
	}
}

func TestNewServerToolMetadataAndStatus(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	if _, err := NewServer(RuntimeOptions{}).Connect(ctx, serverTransport, nil); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "metadata-test", Version: "1"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 16 {
		t.Fatalf("tool count = %d, want 16", len(listed.Tools))
	}
	byName := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
		if tool.Name != "get_handoff" {
			if tool.Annotations == nil || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
				t.Fatalf("tool %s missing closed-world annotation: %#v", tool.Name, tool.Annotations)
			}
		}
	}
	for _, name := range []string{"get_structure", "get_dependencies", "get_diff", "find_file", "get_importers", "status", "list_projects", "get_activity", "get_hubs", "get_file_context", "get_working_set", "list_skills", "get_skill"} {
		if tool := byName[name]; tool == nil || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			t.Fatalf("tool %s missing read-only annotation: %#v", name, tool)
		}
	}
	for _, name := range []string{"start_watch", "stop_watch"} {
		tool := byName[name]
		if tool == nil || tool.Annotations == nil || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint {
			t.Fatalf("tool %s missing non-destructive annotation: %#v", name, tool)
		}
	}
	if byName["get_handoff"].Annotations != nil {
		t.Fatalf("get_handoff must remain unannotated: %#v", byName["get_handoff"].Annotations)
	}
	for _, name := range []string{"get_dependencies", "list_skills"} {
		schema, err := json.Marshal(byName[name].InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(schema), `"depth"`) {
			t.Fatalf("%s schema exposes irrelevant depth: %s", name, schema)
		}
	}

	status, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "status"})
	if err != nil {
		t.Fatal(err)
	}
	statusOut := resultText(t, status)
	if !strings.Contains(statusOut, "codemap MCP server "+buildinfo.Current()) {
		t.Fatalf("status version is not exact: %s", statusOut)
	}
	for name := range byName {
		if !strings.Contains(statusOut, name) {
			t.Fatalf("status omitted registered tool %s:\n%s", name, statusOut)
		}
	}
}

func TestHandleStatusReportsSelectedRoots(t *testing.T) {
	root := t.TempDir()
	selection, err := projectpath.Select(root)
	if err != nil {
		t.Fatal(err)
	}
	result, _, err := handleStatus(context.Background(), nil, StatusInput{Path: root})
	if err != nil {
		t.Fatal(err)
	}
	out := resultText(t, result)
	for _, want := range []string{
		"Project root: " + selection.ProjectRoot,
		"Setup root: " + selection.SetupRoot,
		"Runtime root: " + selection.RuntimeRoot,
		"Selection source: project",
		"Config path: " + filepath.Join(selection.SetupRoot, ".codemap", "config.json"),
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("status missing %q:\n%s", want, out)
		}
	}
}

func TestGetDependenciesUsesSingleAdvertisedRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte("package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println() }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	call := func(t *testing.T, roots []*mcp.Root, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		serverTransport, clientTransport := mcp.NewInMemoryTransports()
		if _, err := NewServer(RuntimeOptions{}).Connect(ctx, serverTransport, nil); err != nil {
			t.Fatal(err)
		}
		client := mcp.NewClient(&mcp.Implementation{Name: "roots-test", Version: "1"}, nil)
		client.AddRoots(roots...)
		session, err := client.Connect(ctx, clientTransport, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer session.Close()
		result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_dependencies", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}

	rootURI := (&url.URL{Scheme: "file", Path: root}).String()
	result := call(t, []*mcp.Root{{URI: rootURI}}, nil)
	if result.IsError {
		t.Fatalf("single root should supply omitted path: %s", resultText(t, result))
	}

	multiple := call(t, []*mcp.Root{{URI: rootURI}, {URI: "file:///tmp/other"}}, nil)
	if !multiple.IsError || !strings.Contains(resultText(t, multiple), "path is required: client advertised 2 roots") {
		t.Fatalf("multiple roots should fail closed: %s", resultText(t, multiple))
	}

	for _, test := range []struct {
		name  string
		roots []*mcp.Root
		want  string
	}{
		{name: "zero", want: "path is required: client advertised 0 roots"},
		{name: "unsupported URI", roots: []*mcp.Root{{URI: "https://example.com/repo"}}, want: "path is required: the single client root is not an absolute local file URI"},
		{name: "malformed URI", roots: []*mcp.Root{{URI: "file:///%zz"}}, want: "path is required: the single client root is not an absolute local file URI"},
		{name: "relative URI", roots: []*mcp.Root{{URI: "file:relative/repo"}}, want: "path is required: the single client root is not an absolute local file URI"},
		{name: "missing directory", roots: []*mcp.Root{{URI: "file:///definitely/not/a/codemap/root"}}, want: "path is required: the single client root is not an accessible directory"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := call(t, test.roots, nil)
			if !got.IsError || !strings.Contains(resultText(t, got), test.want) {
				t.Fatalf("root resolution = %q, want error containing %q", resultText(t, got), test.want)
			}
		})
	}

	if _, invalid := resolveSingleClientRoot(nil); invalid == nil || !strings.Contains(resultText(t, invalid), "path is required: the single client root is missing") {
		t.Fatalf("nil sole root should fail closed, got %#v", invalid)
	}

	explicit := call(t, []*mcp.Root{{URI: "https://example.com/not-local"}, {URI: "file:///tmp/other"}}, map[string]any{"path": root})
	if explicit.IsError {
		t.Fatalf("explicit path must win without consulting roots: %s", resultText(t, explicit))
	}
}

func TestHandleGetDependenciesReturnsCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, _, err := handleGetDependencies(ctx, nil, DependenciesInput{Path: t.TempDir()})
	if result != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("handleGetDependencies() = (%#v, %v), want context.Canceled transport error", result, err)
	}
}

func TestHandleFindFileExplainsOnlyFilteredMatches(t *testing.T) {
	tests := []struct {
		name     string
		config   string
		wantHint bool
		wantPath bool
	}{
		{
			name:     "default guidance",
			config:   `{"only":["go"]}`,
			wantHint: true,
			wantPath: true,
		},
		{
			name:   "guidance disabled",
			config: `{"only":["go"],"guidance":{"missing_extension_hints":false}}`,
		},
		{
			name:   "extension ignored",
			config: `{"only":["go"],"guidance":{"ignored_extensions":["proto"]}}`,
		},
		{
			name:   "explicitly excluded",
			config: `{"only":["go"],"exclude":["schema.proto"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, ".codemap"), 0o755); err != nil {
				t.Fatal(err)
			}
			configPath := filepath.Join(root, ".codemap", "config.json")
			if err := os.WriteFile(configPath, []byte(tt.config), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "schema.proto"), []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
				t.Fatal(err)
			}

			res, _, err := handleFindFile(context.Background(), nil, FindInput{Path: root, Pattern: "schema"})
			if err != nil {
				t.Fatalf("handleFindFile error: %v", err)
			}
			out := resultText(t, res)
			hasHint := strings.Contains(out, "by `only` config:")
			if hasHint != tt.wantHint {
				t.Fatalf("hint presence = %v, want %v:\n%s", hasHint, tt.wantHint, out)
			}
			if strings.Contains(out, "schema.proto") != tt.wantPath {
				t.Fatalf("path presence = %v, want %v:\n%s", strings.Contains(out, "schema.proto"), tt.wantPath, out)
			}
			if !tt.wantHint && out != "No files found matching 'schema'" {
				t.Fatalf("unexpected plain miss output: %s", out)
			}
			gotConfig, err := os.ReadFile(configPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(gotConfig) != tt.config {
				t.Fatalf("config changed: got %q, want %q", gotConfig, tt.config)
			}
		})
	}
}

func TestHandleListSkillsRejectsMalformedWorktreeSetup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, _, err := handleListSkills(context.Background(), nil, SkillsListInput{Path: root})
	if err != nil {
		t.Fatalf("handleListSkills() error: %v", err)
	}
	if !res.IsError || !strings.Contains(resultText(t, res), "resolve linked worktree setup") {
		t.Fatalf("expected bounded root-resolution error, got:\n%s", resultText(t, res))
	}
}

func TestProjectToolsRejectInvalidWorktreeMetadataBeforeAccess(t *testing.T) {
	type toolCall func(string) (*mcp.CallToolResult, error)
	tools := map[string]toolCall{
		"analysis": func(path string) (*mcp.CallToolResult, error) {
			result, _, err := handleGetStructure(context.Background(), nil, PathInput{Path: path})
			return result, err
		},
		"file context": func(path string) (*mcp.CallToolResult, error) {
			result, _, err := handleGetFileContext(context.Background(), nil, ImportersInput{Path: path, File: "main.go"})
			return result, err
		},
		"handoff read": func(path string) (*mcp.CallToolResult, error) {
			result, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: path, Latest: true})
			return result, err
		},
		"handoff save": func(path string) (*mcp.CallToolResult, error) {
			result, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: path, Save: true})
			return result, err
		},
		"watch state": func(path string) (*mcp.CallToolResult, error) {
			result, _, err := handleGetWorkingSet(context.Background(), nil, WatchInput{Path: path})
			return result, err
		},
	}

	fixtures := map[string]func(string) error{
		"malformed gitfile": func(root string) error {
			return os.WriteFile(filepath.Join(root, ".git"), []byte("not a gitdir\n"), 0o644)
		},
		"inaccessible gitdir": func(root string) error {
			return os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: missing-admin-dir\n"), 0o644)
		},
	}

	for fixtureName, makeFixture := range fixtures {
		for toolName, call := range tools {
			t.Run(fixtureName+"/"+toolName, func(t *testing.T) {
				root := t.TempDir()
				if err := makeFixture(root); err != nil {
					t.Fatal(err)
				}

				result, err := call(root)
				if err != nil {
					t.Fatalf("tool returned transport error: %v", err)
				}
				if !result.IsError || !strings.Contains(resultText(t, result), "resolve linked worktree setup") {
					t.Fatalf("expected bounded root-resolution error, got:\n%s", resultText(t, result))
				}
				if _, err := os.Lstat(filepath.Join(root, ".codemap")); !os.IsNotExist(err) {
					t.Fatalf("tool accessed mutable storage before root validation: %v", err)
				}
			})
		}
	}
}

func TestHandoffSaveRejectsSymlinkedLinkedRuntimeBeforeWrite(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks may require elevated privileges")
	}
	for name, withPrimarySetup := range map[string]bool{
		"with primary setup":    true,
		"without primary setup": false,
	} {
		t.Run(name, func(t *testing.T) {
			assertHandoffSaveRejectsSymlinkedLinkedRuntime(t, withPrimarySetup)
		})
	}
}

func TestFormatOnlyFilterHintOffersSortedAgentChoices(t *testing.T) {
	matches := []scanner.FileInfo{
		{Path: "schema.sum", Ext: ".sum"},
		{Path: "schema.proto", Ext: ".proto"},
	}

	out := formatOnlyFilterHint("schema", matches)
	want := "Tell your agent: “include suggestions for proto, sum”, “ignore suggestions for proto, sum”, or “disable suggestions for this repo”."
	if !strings.Contains(out, want) {
		t.Fatalf("missing concise agent choices:\n%s", out)
	}
	if strings.Contains(out, ".codemap/config.json") || strings.Contains(out, "guidance.") {
		t.Fatalf("response exposes config implementation details:\n%s", out)
	}
}

func assertHandoffSaveRejectsSymlinkedLinkedRuntime(t *testing.T, withPrimarySetup bool) {
	t.Helper()
	primary := t.TempDir()
	gitDir := filepath.Join(primary, ".git", "worktrees", "linked")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if withPrimarySetup {
		if err := os.MkdirAll(filepath.Join(primary, ".codemap"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	linked := t.TempDir()
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	sentinel := filepath.Join(target, "sentinel")
	if err := os.WriteFile(sentinel, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(linked, ".codemap")); err != nil {
		t.Fatal(err)
	}

	result, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: linked, Save: true})
	if err != nil {
		t.Fatalf("handleGetHandoff() error: %v", err)
	}
	if !result.IsError || !strings.Contains(resultText(t, result), "unsafe Codemap storage") {
		t.Fatalf("expected unsafe runtime-storage error, got:\n%s", resultText(t, result))
	}
	data, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "unchanged" {
		t.Fatalf("sentinel changed through runtime symlink: %q", data)
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "sentinel" {
		t.Fatalf("runtime symlink target was modified: %#v", entries)
	}
}

func TestHandleListSkillsResolvesWorktreeSetupPerInputPath(t *testing.T) {
	makeFixture := func(name string) string {
		primary := filepath.Join(t.TempDir(), "primary")
		gitDir := filepath.Join(primary, ".git", "worktrees", name)
		skillsDir := filepath.Join(primary, ".codemap", "skills")
		if err := os.MkdirAll(gitDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(skillsDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(gitDir, "commondir"), []byte("../..\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		skill := "---\nname: " + name + "\ndescription: per-path fixture\n---\n\n# Fixture\n"
		if err := os.WriteFile(filepath.Join(skillsDir, name+".md"), []byte(skill), 0o644); err != nil {
			t.Fatal(err)
		}
		linked := filepath.Join(t.TempDir(), "linked")
		if err := os.MkdirAll(linked, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("gitdir: "+gitDir+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return linked
	}

	linkedA := makeFixture("primary-a")
	linkedB := makeFixture("primary-b")
	resA, _, err := handleListSkills(context.Background(), nil, SkillsListInput{Path: linkedA})
	if err != nil {
		t.Fatalf("handleListSkills(A) error: %v", err)
	}
	resB, _, err := handleListSkills(context.Background(), nil, SkillsListInput{Path: linkedB})
	if err != nil {
		t.Fatalf("handleListSkills(B) error: %v", err)
	}
	outA := resultText(t, resA)
	outB := resultText(t, resB)
	if !strings.Contains(outA, "primary-a") || strings.Contains(outA, "primary-b") || !strings.Contains(outB, "primary-b") || strings.Contains(outB, "primary-a") {
		t.Fatalf("per-path skill selection failed:\nA:\n%s\nB:\n%s", outA, outB)
	}
}

func TestHandleGetStructureUsesStateHubs(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".codemap"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pkg", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	state := watch.State{
		UpdatedAt: time.Now(),
		Hubs:      []string{"pkg/main.go"},
		Importers: map[string][]string{"pkg/main.go": {"a.go", "b.go", "c.go"}},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".codemap", "state.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	res, _, err := handleGetStructure(context.Background(), nil, PathInput{Path: root})
	if err != nil {
		t.Fatalf("handleGetStructure error: %v", err)
	}
	out := resultText(t, res)
	checks := []string{"HUB FILES", "pkg/main.go", "3 importers"}
	for _, check := range checks {
		if !strings.Contains(out, check) {
			t.Fatalf("expected output to contain %q, got:\n%s", check, out)
		}
	}
}

func TestHandleGetHandoffValidationAndLatest(t *testing.T) {
	root := t.TempDir()

	res, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: root, Prefix: true, Delta: true})
	if err != nil {
		t.Fatalf("handleGetHandoff validation error: %v", err)
	}
	if !strings.Contains(resultText(t, res), "mutually exclusive") {
		t.Fatalf("expected mutual exclusion error, got:\n%s", resultText(t, res))
	}

	missing, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: root, Latest: true})
	if err != nil {
		t.Fatalf("handleGetHandoff latest error: %v", err)
	}
	if !strings.Contains(resultText(t, missing), "No saved handoff found") {
		t.Fatalf("expected missing latest message, got:\n%s", resultText(t, missing))
	}

	artifact := &handoff.Artifact{
		SchemaVersion: handoff.SchemaVersion,
		Branch:        "feature/test",
		BaseRef:       "main",
		Prefix:        handoff.PrefixSnapshot{FileCount: 7},
		Delta:         handoff.DeltaSnapshot{Changed: []handoff.FileStub{{Path: "main.go"}}},
	}
	if err := handoff.WriteLatest(root, artifact); err != nil {
		t.Fatalf("WriteLatest error: %v", err)
	}

	jsonRes, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: root, Latest: true, JSON: true, Prefix: true})
	if err != nil {
		t.Fatalf("handleGetHandoff latest json error: %v", err)
	}
	jsonOut := resultText(t, jsonRes)
	if !strings.Contains(jsonOut, "\"file_count\": 7") {
		t.Fatalf("expected prefix JSON payload, got:\n%s", jsonOut)
	}
}

func TestHandleGetHandoffRejectsInvalidSince(t *testing.T) {
	root := t.TempDir()

	invalid, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: root, Since: "later"})
	if err != nil {
		t.Fatalf("unexpected error for invalid since result: %v", err)
	}
	if !strings.Contains(resultText(t, invalid), "Invalid since duration") {
		t.Fatalf("expected invalid duration message, got:\n%s", resultText(t, invalid))
	}

	zero, _, err := handleGetHandoff(context.Background(), nil, HandoffInput{Path: root, Since: "0s"})
	if err != nil {
		t.Fatalf("unexpected error for zero since result: %v", err)
	}
	if !strings.Contains(resultText(t, zero), "must be > 0") {
		t.Fatalf("expected non-positive duration message, got:\n%s", resultText(t, zero))
	}
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("expected non-nil result")
	}
	if len(res.Content) != 1 {
		t.Fatalf("expected exactly one content item, got %d", len(res.Content))
	}
	text, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("expected text content, got %T", res.Content[0])
	}
	return text.Text
}
