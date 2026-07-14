# 🛑 STOP — Run codemap before ANY task

## Repo Root Discovery

Run Codemap anywhere inside the repository. Repo-scoped commands and hooks
recover the nearest git root automatically.

Standard linked Git worktrees automatically reuse `.codemap/config.json` and
project skills from their primary worktree. The parent process should give the
agent the linked worktree's absolute path; normal commands need no setup flag:

```bash
git worktree add <path> -b <branch> <base>
codemap -C /tmp/feature-worktree .
```

Central policy is inherited, while mutable handoff, watcher, and hook/session
state stays in the linked worktree. Independent clones remain unrelated and
must use explicit `--setup-root /path/to/original` when they need to share
setup. `-C`/`--project-root` selects only the code repository.

```bash
codemap .                     # Project structure
codemap --deps                # How files connect
codemap --diff                # What changed vs main
codemap --diff --ref <branch> # Changes vs specific branch
```

## Required Usage

**BEFORE starting any task**, run `codemap .` first.

**ALWAYS run `codemap --deps` when:**
- User asks how something works
- Refactoring or moving code
- Tracing imports or dependencies

**ALWAYS run `codemap --diff` when:**
- Reviewing or summarizing changes
- Before committing code
- User asks what changed
- Use `--ref <branch>` when comparing against something other than main
