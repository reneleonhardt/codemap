package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"codemap/internal/projectpath"
)

// GlobalRootOptions are invocation-wide roots extracted before command parsing.
type GlobalRootOptions struct {
	Directory string
	SetupRoot string
}

// Active reports whether this invocation overrides either root.
func (o GlobalRootOptions) Active() bool {
	return o.Directory != "" || o.SetupRoot != ""
}

// InvocationRoots separates the repository being analyzed from the repository
// whose Codemap setup and state are reused.
type InvocationRoots struct {
	Project string
	Setup   string
	Runtime string
	Source  projectpath.Source
}

// ParseGlobalRootOptions extracts root options wherever they appear before --.
func ParseGlobalRootOptions(args []string) (GlobalRootOptions, []string, error) {
	var opts GlobalRootOptions
	remaining := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}

		switch {
		case arg == "-C" || arg == "--project-root":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return GlobalRootOptions{}, nil, fmt.Errorf("%s requires a path", arg)
			}
			i++
			opts.Directory = args[i]
		case strings.HasPrefix(arg, "--project-root="):
			opts.Directory = strings.TrimPrefix(arg, "--project-root=")
			if strings.TrimSpace(opts.Directory) == "" {
				return GlobalRootOptions{}, nil, fmt.Errorf("--project-root requires a path")
			}
		case arg == "--setup-root":
			if i+1 >= len(args) || strings.TrimSpace(args[i+1]) == "" {
				return GlobalRootOptions{}, nil, fmt.Errorf("--setup-root requires a path")
			}
			i++
			opts.SetupRoot = args[i]
		case strings.HasPrefix(arg, "--setup-root="):
			opts.SetupRoot = strings.TrimPrefix(arg, "--setup-root=")
			if strings.TrimSpace(opts.SetupRoot) == "" {
				return GlobalRootOptions{}, nil, fmt.Errorf("--setup-root requires a path")
			}
		default:
			remaining = append(remaining, arg)
		}
	}

	return opts, remaining, nil
}

// ResolveGlobalRoots resolves both inputs with nearest-repository recovery.
// Relative setup roots are interpreted after -C, from the recovered project.
func ResolveGlobalRoots(opts GlobalRootOptions, launchDir string) (InvocationRoots, error) {
	projectInput := opts.Directory
	if projectInput == "" {
		projectInput = launchDir
	} else if !filepath.IsAbs(projectInput) {
		projectInput = filepath.Join(launchDir, projectInput)
	}

	projectRoot, projectFound, err := ResolveNearestGitRoot(projectInput)
	if err != nil {
		return InvocationRoots{}, fmt.Errorf("resolve project root: %w", err)
	}
	if opts.Active() && !projectFound {
		return InvocationRoots{}, fmt.Errorf("resolve project root: %q is not inside a Git repository", projectInput)
	}

	setupRoot := projectRoot
	runtimeRoot := projectRoot
	source := projectpath.SourceProject
	if opts.SetupRoot != "" {
		setupInput := opts.SetupRoot
		if !filepath.IsAbs(setupInput) {
			setupInput = filepath.Join(projectRoot, setupInput)
		}
		var setupFound bool
		setupRoot, setupFound, err = ResolveNearestGitRoot(setupInput)
		if err != nil {
			return InvocationRoots{}, fmt.Errorf("resolve setup root: %w", err)
		}
		if !setupFound {
			return InvocationRoots{}, fmt.Errorf("resolve setup root: %q is not inside a Git repository", setupInput)
		}
		runtimeRoot = setupRoot
		source = projectpath.SourceExplicit
	} else {
		selection, selectErr := projectpath.Select(projectRoot)
		if selectErr != nil {
			return InvocationRoots{}, fmt.Errorf("resolve setup root: %w", selectErr)
		}
		setupRoot = selection.SetupRoot
		runtimeRoot = selection.RuntimeRoot
		source = selection.Source
	}
	if err := validateCodemapStorageRoot(setupRoot); err != nil {
		return InvocationRoots{}, fmt.Errorf("resolve setup root: %w", err)
	}

	return InvocationRoots{Project: projectRoot, Setup: setupRoot, Runtime: runtimeRoot, Source: source}, nil
}

func validateCodemapStorageRoot(root string) error {
	dir := filepath.Join(root, ".codemap")
	info, err := os.Lstat(dir)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("unsafe Codemap storage %q: expected a real directory", dir)
	}
	return nil
}

// ValidateProjectPath returns the caller's absolute path only after automatic
// project selection succeeds. Keeping the exact path preserves commands that
// intentionally analyze a repository subdirectory.
func ValidateProjectPath(path string) (string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if _, err := projectpath.Select(absPath); err != nil {
		return "", err
	}
	return absPath, nil
}

// ResolveNearestGitRoot returns the nearest ancestor directory that contains a
// .git entry. It accepts both .git directories and .git files used by linked
// worktrees. When no repository root exists, it returns the absolute input
// path and found=false.
func ResolveNearestGitRoot(path string) (resolved string, found bool, err error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return "", false, err
	}
	canonicalPath, err := filepath.EvalSymlinks(absPath)
	if err != nil {
		return "", false, err
	}
	info, err := os.Stat(canonicalPath)
	if err != nil {
		return "", false, err
	}
	if !info.IsDir() {
		return "", false, fmt.Errorf("%q is not a directory", path)
	}

	for current := canonicalPath; ; current = filepath.Dir(current) {
		valid, err := validGitMarker(current)
		if err != nil {
			return "", false, err
		}
		if valid {
			return current, true, nil
		}

		parent := filepath.Dir(current)
		if parent == current {
			return canonicalPath, false, nil
		}
	}
}

func validGitMarker(root string) (bool, error) {
	marker := filepath.Join(root, ".git")
	info, err := os.Lstat(marker)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.IsDir() {
		return true, nil
	}
	if info.Mode().IsRegular() {
		return true, nil
	}
	return false, fmt.Errorf("invalid Git marker %q: expected a directory or regular gitfile", marker)
}
