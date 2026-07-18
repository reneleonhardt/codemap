// Package watch provides a file system watcher daemon for live code graph updates
package watch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"codemap/config"
	"codemap/internal/projectpath"
	"codemap/limits"
	"codemap/scanner"

	"github.com/fsnotify/fsnotify"
)

// Daemon is the watch daemon that keeps the graph updated
type Daemon struct {
	root     string
	graph    *Graph
	watcher  *fsnotify.Watcher
	gitCache *scanner.GitIgnoreCache
	eventLog string // path to event log file
	verbose  bool
	done     chan struct{}
}

// NewDaemon creates a new watch daemon for the given root
func NewDaemon(root string, verbose bool) (*Daemon, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("invalid root path: %w", err)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("failed to create watcher: %w", err)
	}

	gitCache := scanner.NewGitIgnoreCache(root)

	// Check if git repo (fast, one-time)
	isGitRepo := false
	if _, err := os.Stat(filepath.Join(absRoot, ".git")); err == nil {
		isGitRepo = true
	}

	d := &Daemon{
		root:     absRoot,
		watcher:  watcher,
		gitCache: gitCache,
		verbose:  verbose,
		done:     make(chan struct{}),
		eventLog: filepath.Join(projectpath.RuntimeCodemapDir(absRoot), "events.log"),
		graph: &Graph{
			Root:            absRoot,
			Files:           make(map[string]*scanner.FileInfo),
			ConfiguredFiles: make(map[string]struct{}),
			DepCtx:          make(map[string]*DepContext),
			State:           make(map[string]*FileState),
			Events:          make([]Event, 0),
			WorkingSet:      NewWorkingSet(),
			IsGitRepo:       isGitRepo,
		},
	}

	return d, nil
}

// Start begins watching and returns immediately
func (d *Daemon) Start() error {
	// Ensure .codemap directory exists
	codemapDir := projectpath.RuntimeCodemapDir(d.root)
	if err := os.MkdirAll(codemapDir, 0755); err != nil {
		return fmt.Errorf("failed to create .codemap dir: %w", err)
	}

	// Initial full scan
	if err := d.fullScan(); err != nil {
		return fmt.Errorf("initial scan failed: %w", err)
	}

	// Compute dependency graph (best effort). Skip on very large repos to avoid
	// expensive startup memory/CPU spikes in background hook flows.
	fileCount := d.ConfiguredFileCount()
	if shouldComputeDependencyGraph(fileCount) {
		d.computeDeps()
	} else {
		d.markGraphLifecycle(graphLifecycleSkippedSize)
		if d.verbose {
			fmt.Printf("[watch] Skipping dependency graph for large repo (%d files)\n", fileCount)
		}
	}

	// Add directories to watcher
	if err := d.addWatchDirs(); err != nil {
		return fmt.Errorf("failed to add watch dirs: %w", err)
	}

	// Write initial state for hooks to read immediately
	d.writeState()

	// Start event loop
	go d.eventLoop()

	return nil
}

// Stop gracefully shuts down the daemon
func (d *Daemon) Stop() {
	close(d.done)
	d.watcher.Close()
}

// GetGraph returns the current graph (thread-safe)
func (d *Daemon) GetGraph() *Graph {
	return d.graph
}

// GetEvents returns recent events (thread-safe)
func (d *Daemon) GetEvents(limit int) []Event {
	d.graph.mu.RLock()
	defer d.graph.mu.RUnlock()

	events := d.graph.Events
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}

	// Return a copy
	result := make([]Event, len(events))
	copy(result, events)
	return result
}

// FileCount returns current tracked file count
func (d *Daemon) FileCount() int {
	d.graph.mu.RLock()
	defer d.graph.mu.RUnlock()
	return len(d.graph.Files)
}

// ConfiguredFileCount returns the number of files included by the active
// project filters. FileCount intentionally continues to report all tracked
// files for watch/activity consumers.
func (d *Daemon) ConfiguredFileCount() int {
	d.graph.mu.RLock()
	defer d.graph.mu.RUnlock()
	return len(d.graph.ConfiguredFiles)
}

func shouldComputeDependencyGraph(fileCount int) bool {
	return fileCount <= limits.LargeRepoFileCount
}

// WriteInitialState writes state after initial scan (for hooks)
func (d *Daemon) WriteInitialState() {
	d.writeState()
}

// fullScan does a complete scan of the project
func (d *Daemon) fullScan() error {
	start := time.Now()

	files, err := scanner.ScanFiles(d.root, d.gitCache, nil, nil)
	if err != nil {
		return err
	}
	configuredFiles, err := scanner.ScanConfiguredFiles(d.root, d.gitCache)
	if err != nil {
		return err
	}

	d.graph.mu.Lock()
	d.graph.Files = make(map[string]*scanner.FileInfo)
	d.graph.ConfiguredFiles = make(map[string]struct{}, len(configuredFiles))
	d.graph.State = make(map[string]*FileState)
	for i := range files {
		f := &files[i]
		d.graph.Files[f.Path] = f
		// Cache line count for delta calculations (fast: ~1ms per file)
		if lines := countLines(filepath.Join(d.root, f.Path)); lines > 0 {
			d.graph.State[f.Path] = &FileState{Lines: lines, Size: f.Size}
		}
	}
	for _, file := range configuredFiles {
		d.graph.ConfiguredFiles[file.Path] = struct{}{}
	}
	d.graph.LastScan = time.Now()
	d.graph.mu.Unlock()

	if d.verbose {
		fmt.Printf("[watch] Full scan: %d files in %v\n", len(files), time.Since(start))
	}

	return nil
}

func (d *Daemon) isConfiguredFile(path string) bool {
	cfg := config.Load(d.root)
	return scanner.MatchesFilters(path, filepath.Ext(path), cfg.Only, cfg.Exclude)
}

// computeDeps builds the file-to-file dependency graph
func (d *Daemon) computeDeps() {
	d.computeDepsWith(scanner.BuildFileGraph)
}

func (d *Daemon) computeDepsWith(build func(string) (*scanner.FileGraph, error)) {
	d.computeDepsWithBeforePublish(build, nil)
}

func (d *Daemon) computeDepsWithBeforePublish(build func(string) (*scanner.FileGraph, error), beforePublish func()) {
	start := time.Now()
	buildConfig := config.Load(d.root)
	d.graph.mu.RLock()
	configuredBefore := make([]string, 0, len(d.graph.ConfiguredFiles))
	for file := range d.graph.ConfiguredFiles {
		configuredBefore = append(configuredBefore, file)
	}
	generationBefore := d.graph.graphGeneration
	d.graph.mu.RUnlock()

	// Build file graph (internal file-to-file dependencies)
	fg, err := build(d.root)
	if err != nil || fg == nil || (len(configuredBefore) > 0 && len(fg.Imports) == 0 && len(fg.Importers) == 0) {
		d.markGraphLifecycle(graphLifecycleFailed)
		if d.verbose {
			fmt.Printf("[watch] File graph unavailable: %v\n", err)
		}
		return
	}
	if beforePublish != nil {
		beforePublish()
	}

	d.graph.mu.Lock()
	defer d.graph.mu.Unlock()
	configuredAfter := make([]string, 0, len(d.graph.ConfiguredFiles))
	for file := range d.graph.ConfiguredFiles {
		configuredAfter = append(configuredAfter, file)
	}
	currentConfig := config.Load(d.root)
	if d.graph.graphGeneration != generationBefore ||
		ConfiguredInventoryFingerprint(configuredBefore) != ConfiguredInventoryFingerprint(configuredAfter) ||
		graphFilterFingerprint(buildConfig) != graphFilterFingerprint(currentConfig) {
		d.markGraphLifecycleLocked(newGraphState(d.root, currentConfig, graphLifecycleStale, time.Time{}, nil))
		return
	}
	state := newGraphState(d.root, buildConfig, graphLifecycleAvailable, time.Now(), configuredBefore)

	// Convert FileGraph to DepContext map
	d.graph.DepCtx = make(map[string]*DepContext)
	d.graph.FileGraph = fg

	for path := range d.graph.Files {
		ctx := &DepContext{
			Imports:   fg.Imports[path],
			Importers: fg.Importers[path],
		}
		d.graph.DepCtx[path] = ctx
	}

	d.graph.HasDeps = true
	d.graph.GraphState = state

	hubCount := len(fg.HubFiles())
	if d.verbose {
		fmt.Printf("[watch] File graph: %d files, %d hubs in %v\n", len(d.graph.Files), hubCount, time.Since(start))
	}
}

func (d *Daemon) markGraphLifecycle(status GraphLifecycle) {
	state := newGraphState(d.root, config.Load(d.root), status, time.Time{}, nil)
	d.graph.mu.Lock()
	defer d.graph.mu.Unlock()
	d.markGraphLifecycleLocked(state)
}

func (d *Daemon) markGraphLifecycleLocked(state GraphState) {
	d.graph.graphGeneration++
	d.graph.GraphState = state
	d.graph.HasDeps = false
	d.graph.FileGraph = nil
	d.graph.DepCtx = make(map[string]*DepContext)
}

// addWatchDirs recursively adds directories to the watcher
func (d *Daemon) addWatchDirs() error {
	return filepath.Walk(d.root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip errors
		}

		absPath, _ := filepath.Abs(path)

		// Skip hidden directories and common ignores
		name := info.Name()
		if info.IsDir() {
			if d.gitCache != nil {
				d.gitCache.EnsureDir(absPath)
				// Honor nested .gitignore rules so ignored subtrees are never watched.
				if path != d.root && d.gitCache.ShouldIgnore(absPath) {
					return filepath.SkipDir
				}
			}
			if strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor" {
				return filepath.SkipDir
			}
			return d.watcher.Add(path)
		}
		return nil
	})
}
