package scanner

import (
	"bufio"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	rustCoverageStatus = "partial"
	rustCoverageNote   = "Rust macro-generated, string-routed, renamed-dependency, and #[path] module edges may be unresolved"
)

// GraphCoverage describes known dependency-graph blind spots.
type GraphCoverage struct {
	Status string   `json:"status,omitempty"`
	Notes  []string `json:"notes,omitempty"`
}

type rustTarget struct {
	rootFile  string
	sourceDir string
}

type rustPackage struct {
	crateID string
	root    string
	lib     *rustTarget
	targets []rustTarget
}

type rustWorkspaceIndex struct {
	byCrateID   map[string][]rustPackage
	packages    []rustPackage
	moduleDecls map[string]map[string]bool
}

type cargoManifest struct {
	Package struct {
		Name          string `toml:"name"`
		Build         string `toml:"build"`
		BuildDisabled bool
		AutoLib       *bool `toml:"autolib"`
		AutoBins      *bool `toml:"autobins"`
		AutoExamples  *bool `toml:"autoexamples"`
		AutoTests     *bool `toml:"autotests"`
		AutoBenches   *bool `toml:"autobenches"`
	} `toml:"package"`
	Workspace struct {
		Members []string `toml:"members"`
		Exclude []string `toml:"exclude"`
	} `toml:"workspace"`
	Lib struct {
		Path string `toml:"path"`
	} `toml:"lib"`
	LibPresent bool
	Targets    []cargoTarget
}

type cargoTarget struct {
	Kind string
	Name string
	Path string
}

func buildRustWorkspaceIndex(root string, analyses []FileAnalysis) *rustWorkspaceIndex {
	index := &rustWorkspaceIndex{
		byCrateID:   make(map[string][]rustPackage),
		moduleDecls: make(map[string]map[string]bool),
	}
	for _, analysis := range analyses {
		if analysis.Language != "rust" {
			continue
		}
		for _, ref := range analysis.References {
			if ref.Kind != "rust-module" || rustModuleUsesPathAttribute(root, analysis.Path, ref.Line) {
				continue
			}
			if index.moduleDecls[analysis.Path] == nil {
				index.moduleDecls[analysis.Path] = make(map[string]bool)
			}
			index.moduleDecls[analysis.Path][ref.Path] = true
		}
	}
	rootManifest, ok := readCargoManifest(filepath.Join(root, "Cargo.toml"))
	if !ok {
		return index
	}

	memberDirs := []string{}
	if rootManifest.Package.Name != "" {
		memberDirs = append(memberDirs, ".")
	}
	for _, member := range rootManifest.Workspace.Members {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(member)))
		if err != nil {
			continue
		}
		for _, match := range matches {
			rel, err := filepath.Rel(root, match)
			if err == nil {
				memberDirs = append(memberDirs, rel)
			}
		}
	}

	excluded := make(map[string]bool)
	for _, pattern := range rootManifest.Workspace.Exclude {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			continue
		}
		for _, match := range matches {
			if rel, err := filepath.Rel(root, match); err == nil {
				excluded[filepath.Clean(rel)] = true
			}
		}
	}

	for _, memberDir := range dedupe(memberDirs) {
		memberDir = filepath.Clean(memberDir)
		if excluded[memberDir] {
			continue
		}
		manifest, ok := readCargoManifest(filepath.Join(root, memberDir, "Cargo.toml"))
		if !ok || manifest.Package.Name == "" {
			continue
		}
		targets, lib := rustPackageTargets(root, memberDir, manifest)
		pkg := rustPackage{
			crateID: strings.ReplaceAll(manifest.Package.Name, "-", "_"),
			root:    memberDir,
			lib:     lib,
			targets: targets,
		}
		index.packages = append(index.packages, pkg)
		index.byCrateID[pkg.crateID] = append(index.byCrateID[pkg.crateID], pkg)
	}

	sort.Slice(index.packages, func(i, j int) bool {
		return len(index.packages[i].root) > len(index.packages[j].root)
	})
	return index
}

func rustPackageTargets(root, memberDir string, manifest cargoManifest) ([]rustTarget, *rustTarget) {
	var targets []rustTarget
	seen := make(map[string]bool)
	add := func(path string) *rustTarget {
		if path == "" {
			return nil
		}
		rootFile := filepath.Clean(filepath.Join(memberDir, filepath.FromSlash(path)))
		info, err := os.Stat(filepath.Join(root, rootFile))
		if err != nil || info.IsDir() {
			return nil
		}
		target := rustTarget{rootFile: rootFile, sourceDir: filepath.Dir(rootFile)}
		if !seen[rootFile] {
			targets = append(targets, target)
			seen[rootFile] = true
		}
		return &target
	}
	addMatches := func(patterns ...string) {
		for _, pattern := range patterns {
			matches, err := filepath.Glob(filepath.Join(root, memberDir, filepath.FromSlash(pattern)))
			if err != nil {
				continue
			}
			for _, match := range matches {
				rel, err := filepath.Rel(filepath.Join(root, memberDir), match)
				if err == nil {
					add(rel)
				}
			}
		}
	}
	addInferred := func(target cargoTarget) {
		var existing []string
		for _, path := range inferredCargoTargetPaths(manifest.Package.Name, target) {
			info, err := os.Stat(filepath.Join(root, memberDir, filepath.FromSlash(path)))
			if err == nil && !info.IsDir() {
				existing = append(existing, path)
			}
		}
		if len(existing) == 1 {
			add(existing[0])
		}
	}

	var lib *rustTarget
	if manifest.LibPresent || manifest.Lib.Path != "" || cargoAutoEnabled(manifest.Package.AutoLib) {
		libPath := manifest.Lib.Path
		if libPath == "" {
			libPath = filepath.Join("src", "lib.rs")
		}
		lib = add(libPath)
	}

	if cargoAutoEnabled(manifest.Package.AutoBins) {
		add(filepath.Join("src", "main.rs"))
		addMatches("src/bin/*.rs", "src/bin/*/main.rs")
	}
	if cargoAutoEnabled(manifest.Package.AutoExamples) {
		addMatches("examples/*.rs", "examples/*/main.rs")
	}
	if cargoAutoEnabled(manifest.Package.AutoTests) {
		addMatches("tests/*.rs", "tests/*/main.rs")
	}
	if cargoAutoEnabled(manifest.Package.AutoBenches) {
		addMatches("benches/*.rs", "benches/*/main.rs")
	}
	if !manifest.Package.BuildDisabled {
		buildPath := manifest.Package.Build
		if buildPath == "" {
			buildPath = "build.rs"
		}
		add(buildPath)
	}
	for _, target := range manifest.Targets {
		if target.Path != "" {
			add(target.Path)
		} else {
			addInferred(target)
		}
	}

	sort.Slice(targets, func(i, j int) bool {
		return targets[i].rootFile < targets[j].rootFile
	})
	return targets, lib
}

func inferredCargoTargetPaths(packageName string, target cargoTarget) []string {
	if target.Name == "" {
		return nil
	}
	switch target.Kind {
	case "bin":
		var paths []string
		if target.Name == packageName {
			paths = append(paths, filepath.Join("src", "main.rs"))
		}
		return append(paths,
			filepath.Join("src", "bin", target.Name+".rs"),
			filepath.Join("src", "bin", target.Name, "main.rs"),
		)
	case "example":
		return []string{
			filepath.Join("examples", target.Name+".rs"),
			filepath.Join("examples", target.Name, "main.rs"),
		}
	case "test":
		return []string{
			filepath.Join("tests", target.Name+".rs"),
			filepath.Join("tests", target.Name, "main.rs"),
		}
	case "bench":
		return []string{
			filepath.Join("benches", target.Name+".rs"),
			filepath.Join("benches", target.Name, "main.rs"),
		}
	default:
		return nil
	}
}

func cargoAutoEnabled(flag *bool) bool {
	return flag == nil || *flag
}

func readCargoManifest(path string) (cargoManifest, bool) {
	var manifest cargoManifest
	file, err := os.Open(path)
	if err != nil {
		return cargoManifest{}, false
	}
	defer file.Close()

	section := ""
	targetIndex := -1
	var pendingKey string
	var pendingValue strings.Builder
	apply := func(key, value string) {
		switch {
		case section == "package" && key == "name":
			manifest.Package.Name = parseCargoString(value)
		case section == "package" && key == "build":
			manifest.Package.Build = parseCargoString(value)
			if enabled, ok := parseCargoBool(value); ok {
				manifest.Package.BuildDisabled = !enabled
			}
		case section == "package" && key == "autolib":
			manifest.Package.AutoLib = parseCargoBoolPtr(value)
		case section == "package" && key == "autobins":
			manifest.Package.AutoBins = parseCargoBoolPtr(value)
		case section == "package" && key == "autoexamples":
			manifest.Package.AutoExamples = parseCargoBoolPtr(value)
		case section == "package" && key == "autotests":
			manifest.Package.AutoTests = parseCargoBoolPtr(value)
		case section == "package" && key == "autobenches":
			manifest.Package.AutoBenches = parseCargoBoolPtr(value)
		case section == "workspace" && key == "members":
			manifest.Workspace.Members = parseCargoStringArray(value)
		case section == "workspace" && key == "exclude":
			manifest.Workspace.Exclude = parseCargoStringArray(value)
		case section == "lib" && key == "path":
			manifest.Lib.Path = parseCargoString(value)
		case targetIndex >= 0 && key == "path":
			manifest.Targets[targetIndex].Path = parseCargoString(value)
		case targetIndex >= 0 && key == "name":
			manifest.Targets[targetIndex].Name = parseCargoString(value)
		}
	}

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(stripCargoComment(scanner.Text()))
		if pendingKey != "" {
			pendingValue.WriteString(line)
			if strings.Contains(line, "]") {
				apply(pendingKey, pendingValue.String())
				pendingKey = ""
				pendingValue.Reset()
			}
			continue
		}
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[[") && strings.HasSuffix(line, "]]") {
			section = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			targetIndex = -1
			switch section {
			case "bin", "example", "test", "bench":
				manifest.Targets = append(manifest.Targets, cargoTarget{Kind: section})
				targetIndex = len(manifest.Targets) - 1
			}
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(strings.Trim(line, "[]"))
			targetIndex = -1
			if section == "lib" {
				manifest.LibPresent = true
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, "[") && !strings.Contains(value, "]") {
			pendingKey = key
			pendingValue.WriteString(value)
			continue
		}
		apply(key, value)
	}
	if scanner.Err() != nil {
		return cargoManifest{}, false
	}
	return manifest, true
}

func stripCargoComment(line string) string {
	inString := false
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && inString {
			escaped = true
			continue
		}
		if r == '"' {
			inString = !inString
			continue
		}
		if r == '#' && !inString {
			return line[:i]
		}
	}
	return line
}

func parseCargoString(value string) string {
	value = strings.TrimSpace(strings.TrimSuffix(value, ","))
	parsed, err := strconv.Unquote(value)
	if err != nil {
		return ""
	}
	return parsed
}

func parseCargoStringArray(value string) []string {
	value = strings.TrimSpace(value)
	value = strings.TrimPrefix(value, "[")
	value = strings.TrimSuffix(value, "]")
	var result []string
	for _, item := range strings.Split(value, ",") {
		if parsed := parseCargoString(item); parsed != "" {
			result = append(result, parsed)
		}
	}
	return result
}

func parseCargoBool(value string) (bool, bool) {
	parsed, err := strconv.ParseBool(strings.TrimSpace(strings.TrimSuffix(value, ",")))
	return parsed, err == nil
}

func parseCargoBoolPtr(value string) *bool {
	parsed, ok := parseCargoBool(value)
	if !ok {
		return nil
	}
	return &parsed
}

func resolveRustReferences(root string, analysis FileAnalysis, idx *fileIndex, workspace *rustWorkspaceIndex) []string {
	var resolved []string
	for _, ref := range analysis.References {
		switch ref.Kind {
		case "rust-module":
			if rustModuleUsesPathAttribute(root, analysis.Path, ref.Line) {
				continue
			}
			if target := resolveRustModule(ref.Path, analysis.Path, idx, workspace); target != "" && target != analysis.Path {
				resolved = append(resolved, target)
			}
		case "rust-path":
			if target := resolveRustPath(ref.Path, analysis.Path, idx, workspace); target != "" && target != analysis.Path {
				resolved = append(resolved, target)
			}
		}
	}
	return dedupe(resolved)
}

func resolveRustModule(name, fromFile string, idx *fileIndex, workspace *rustWorkspaceIndex) string {
	dir := rustModuleDir(fromFile, idx, workspace)
	for _, candidate := range []string{
		filepath.Join(dir, name+".rs"),
		filepath.Join(dir, name, "mod.rs"),
	} {
		if files := idx.byExact[candidate]; len(files) == 1 {
			return files[0]
		}
	}
	return ""
}

func rustModuleDir(fromFile string, idx *fileIndex, workspace *rustWorkspaceIndex) string {
	if target, ok := workspace.targetForFile(fromFile, idx); ok && target.rootFile == filepath.Clean(fromFile) {
		return target.sourceDir
	}
	return rustChildModuleDir(fromFile)
}

func rustChildModuleDir(path string) string {
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	switch base {
	case "mod.rs":
		return dir
	default:
		return filepath.Join(dir, strings.TrimSuffix(base, filepath.Ext(base)))
	}
}

func resolveRustPath(path, fromFile string, idx *fileIndex, workspace *rustWorkspaceIndex) string {
	path = strings.TrimPrefix(strings.TrimSpace(path), "::")
	parts := strings.Split(path, "::")
	if len(parts) < 2 {
		return ""
	}

	var base string
	rootFile := ""
	switch parts[0] {
	case "crate":
		target, ok := workspace.targetForFile(fromFile, idx)
		if !ok {
			return ""
		}
		base = target.sourceDir
		rootFile = target.rootFile
		parts = parts[1:]
	case "self":
		base = rustModuleDir(fromFile, idx, workspace)
		parts = parts[1:]
	case "super":
		base = rustModuleDir(fromFile, idx, workspace)
		for len(parts) > 0 && parts[0] == "super" {
			base = filepath.Dir(base)
			parts = parts[1:]
		}
	default:
		packages := workspace.byCrateID[parts[0]]
		if len(packages) != 1 {
			return ""
		}
		if packages[0].lib == nil || !rustTargetIndexed(*packages[0].lib, idx) {
			return ""
		}
		base = packages[0].lib.sourceDir
		rootFile = packages[0].lib.rootFile
		parts = parts[1:]
	}

	for i := len(parts); i > 0; i-- {
		modulePath := filepath.Join(append([]string{base}, parts[:i]...)...)
		for _, candidate := range []string{modulePath + ".rs", filepath.Join(modulePath, "mod.rs")} {
			if files := idx.byExact[candidate]; len(files) == 1 {
				return files[0]
			}
		}
	}
	if rootFile != "" {
		if files := idx.byExact[rootFile]; len(files) == 1 {
			return files[0]
		}
	}
	return ""
}

func (index *rustWorkspaceIndex) targetForFile(path string, idx *fileIndex) (rustTarget, bool) {
	pkg, ok := index.packageForFile(path)
	if !ok {
		return rustTarget{}, false
	}
	path = filepath.Clean(path)
	for _, target := range pkg.targets {
		if rustTargetIndexed(target, idx) && path == target.rootFile {
			return target, true
		}
	}

	bestDepth := -1
	var best rustTarget
	ambiguous := false
	for _, target := range pkg.targets {
		if !rustTargetIndexed(target, idx) || !index.targetContainsFile(target, path, idx) {
			continue
		}
		depth := pathDepth(target.sourceDir)
		switch {
		case depth > bestDepth:
			bestDepth = depth
			best = target
			ambiguous = false
		case depth == bestDepth && target.rootFile != best.rootFile:
			ambiguous = true
		}
	}
	if bestDepth < 0 || ambiguous {
		return rustTarget{}, false
	}
	return best, true
}

func rustTargetIndexed(target rustTarget, idx *fileIndex) bool {
	files := idx.byExact[target.rootFile]
	return len(files) == 1 && files[0] == target.rootFile
}

func pathWithin(path, dir string) bool {
	path, dir = filepath.Clean(path), filepath.Clean(dir)
	if dir == "." {
		return true
	}
	return path == dir || strings.HasPrefix(path, dir+string(filepath.Separator))
}

func (index *rustWorkspaceIndex) targetContainsFile(target rustTarget, path string, idx *fileIndex) bool {
	if !pathWithin(path, target.sourceDir) {
		return false
	}
	rel, err := filepath.Rel(target.sourceDir, path)
	if err != nil || rel == "." {
		return err == nil
	}
	parts := strings.Split(rel, string(filepath.Separator))
	last := parts[len(parts)-1]
	if last == "mod.rs" {
		parts = parts[:len(parts)-1]
	} else if filepath.Ext(last) == ".rs" {
		parts[len(parts)-1] = strings.TrimSuffix(last, ".rs")
	} else {
		return false
	}
	if len(parts) == 0 {
		return false
	}

	parent := target.rootFile
	for i, name := range parts {
		if !index.moduleDecls[parent][name] {
			return false
		}
		modulePath := filepath.Join(append([]string{target.sourceDir}, parts[:i+1]...)...)
		var next string
		for _, candidate := range []string{modulePath + ".rs", filepath.Join(modulePath, "mod.rs")} {
			if files := idx.byExact[candidate]; len(files) == 1 {
				if next != "" {
					return false
				}
				next = files[0]
			}
		}
		if next == "" {
			return false
		}
		parent = next
	}
	return parent == path
}

func pathDepth(path string) int {
	path = filepath.Clean(path)
	if path == "." {
		return 0
	}
	return strings.Count(path, string(filepath.Separator)) + 1
}

func (index *rustWorkspaceIndex) packageForFile(path string) (rustPackage, bool) {
	path = filepath.Clean(path)
	var rootPackage *rustPackage
	for _, pkg := range index.packages {
		if pkg.root == "." {
			candidate := pkg
			rootPackage = &candidate
			continue
		}
		if path == pkg.root || strings.HasPrefix(path, pkg.root+string(filepath.Separator)) {
			return pkg, true
		}
	}
	if rootPackage != nil {
		return *rootPackage, true
	}
	return rustPackage{}, false
}

func rustModuleUsesPathAttribute(root, file string, line int) bool {
	data, err := os.ReadFile(filepath.Join(root, file))
	if err != nil {
		return false
	}
	lines := strings.Split(string(data), "\n")
	for i := line - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#[path") {
			return true
		}
		if !strings.HasPrefix(trimmed, "#[") {
			return false
		}
	}
	return false
}
