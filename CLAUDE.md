# 🛑 STOP — Run codemap before ANY task

## Repo Root Discovery

Run Codemap anywhere inside the repository. Repo-scoped commands and hooks
recover the nearest git root automatically.

For a temporary worktree that should reuse untracked Codemap state from another
checkout, select both roots explicitly. Either value may be a repository root or
a subdirectory:

```bash
codemap -C /tmp/feature-worktree --setup-root /path/to/original .
```

`-C`/`--project-root` selects the code repository. `--setup-root` selects the
repository containing the `.codemap/` config, state, skills, and handoffs.

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
