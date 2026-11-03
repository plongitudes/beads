# Storage Methodology Evaluation (bd-1y5)

Status: draft recommendation, 2026-10-19. Evaluated at v0.48.1 (commit 7f0ff622b). Related: bd-fxy (daemon), bd-78t (plugin backends).

## Requirements

- Solo user, several machines (including remote/cloud Claude sessions).
- Must behave predictably across git worktrees, including parallel agents in separate worktrees.
- Open to multi-user later depending on the outcome of bd-78t.

## How it works today

SQLite (`.beads/beads.db`, gitignored) is the local working copy and query engine. `.beads/issues.jsonl` is git-tracked and is the transport between machines. A large machinery keeps the two in step: dirty tracking, debounced auto-flush, auto-import on staleness, content hashes, tombstones with a 30-day TTL, orphan/resurrection policies, git hooks (pre-commit, post-merge, post-checkout, pre-push), a git merge driver, an optional sync branch with its own hidden worktree, and the daemon.

Size comparison (non-test Go LOC):

| Area | LOC |
|---|---|
| Sync, merge, import/export, hooks, sync-branch, snapshots, daemon sync loop | ~20–21K |
| Core CRUD and query (sqlite minus sync files, migrations, memory store, types, CRUD commands) | ~17–18K |

The sync layer is larger than the tracker it serves, and it contains two separate 3-way merge engines with two separate base-state files (`sync_base.jsonl` used by `bd sync` in `cmd/bd/sync_merge.go`, and `beads.base.jsonl` used by the daemon and merge driver in `internal/merge/merge.go`).

## Findings

### 1. Issues are global in practice, but stored per-branch

The docs describe issues as branch-scoped (`docs/FAQ.md:25-27`, `docs/GIT_INTEGRATION.md:314-315`). In practice:

- There is one SQLite DB per repo, shared by all worktrees. Worktree discovery prefers `<main repo>/.beads` (`internal/beads/beads.go:566-584`).
- Import is upsert-only (`cmd/bd/sync_import.go:68-147`). Switching branches never removes issues, so the next export writes the other branch's issues into the current branch's JSONL.
- From a linked worktree, `bd sync` pulls the worktree's branch but reads/writes the main checkout's JSONL and commits with `git -C <mainRepoRoot>` onto whatever branch the main checkout has (`cmd/bd/sync_git.go:80-89,198-267,350-388`). Hooks meanwhile stage the worktree's own copy (`cmd/bd/hooks.go:512-544`), which drifts.

Storing global data inside branch-scoped files is the root of most of the complexity: resurrection logic, tombstones, merge drivers, mass-deletion warnings, and the sync-branch workaround all exist to reconcile that mismatch.

### 2. The defect history is concentrated here

Across 79 CHANGELOG versions (0.14.0–0.48.1), roughly 20 distinct worktree fixes landed in 14 releases, about 40 sync fixes, and 26 entries mention resurrection, zombies, or data loss. Bare-repo worktrees and redirect files each regressed more than once (GH#785, GH#827, GH#814, bd-lmqhe).

### 3. SQLite itself is not the problem

The query layer earns its keep: recursive CTEs for ready/blocked/trees/cycles, a materialized `blocked_issues_cache` (752ms → 29ms on 10K issues per `blocked_cache.go:1-40`), WAL mode with `BEGIN IMMEDIATE` and busy retries. Concurrency between processes on one machine is handled reasonably well by SQLite. Contention problems mostly come from multiple processes re-exporting the whole DB to one shared JSONL file.

### 4. The storage interface is not yet a clean seam

`internal/storage/storage.go` defines ~62 methods, but:

- 27 type assertions to `*sqlite.SQLiteStorage` across 19 non-test files outside the package (e.g. `cmd/bd/show.go`, `cmd/bd/compact.go`, `internal/rpc/server_issues_epics.go`, `internal/molecules/molecules.go:152`).
- The importer rejects anything but SQLite (`internal/importer/importer.go:181-183`), and every sync path, Linear, and Jira go through it.
- Sync bookkeeping (dirty set, export hashes, JSONL file hash) and `UnderlyingDB() *sql.DB` are part of the core interface.

`UnderlyingDB()` is also a documented extension point (`docs/EXTENDING.md`, custom tables), so removing it from the interface is a deliberate API break that needs an alternative or an explicit drop.

This matters for any option below, and especially for bd-78t.

### 5. Possible latent bug

`cmd/bd/worktree_cmd.go:205` writes the redirect as a path relative to the worktree's `.beads/`, while `FollowRedirect` resolves relative paths from the parent of `.beads/` (`internal/beads/beads.go:71-74`). The result would be one directory too high. It is mostly masked because worktree discovery checks the main repo's `.beads` before reading the redirect. Not reproduced yet.

## Options

| Option | Worktrees | Multi-machine | Offline | Complexity | Notes |
|---|---|---|---|---|---|
| A. Keep as-is | Poor: global data in per-branch files | Yes, via git | Yes | Very high (~20K LOC sync) | Recurring defect source |
| B. Plain local SQLite, no sync | Excellent if DB lives in git-common-dir | No | Yes | Lowest | Fails the multi-machine requirement on its own |
| C. GitHub Issues as primary store | Excellent: no local state to diverge | Yes, including cloud sessions | No (needs cache) | Medium; network on every command | Issue dependencies are GA with API support ([changelog](https://github.blog/changelog/2025-08-21-dependencies-on-issues/)). Ready-work queries still need a local graph, so in practice this becomes B + a GitHub transport |
| D. Local SQLite + git-ref transport | Excellent | Yes, via git push/pull of a dedicated ref | Yes | Low–medium | Issues live on a ref (e.g. `refs/beads/issues`) written with git plumbing, never checked out into a code branch. Prior art: git-bug stores issues in `refs/bugs/*` |
| E. Dolt (upstream's path) | Good | Yes, Dolt remotes | Yes | Large new dependency | Upstream beads moved to Dolt-only storage around v0.50 (early 2026) ([DoltHub](https://www.dolthub.com/blog/2026-04-15-common-beads-workflows/), [beads docs](https://beads.gascity.com/architecture/dolt)). Rejected: this fork deliberately branched just before that switch to avoid Dolt's weight |

## Recommendation

Keep SQLite as the local store and query engine. Stop storing issues in branch-tracked files. Treat sync as a pluggable transport rather than as part of storage.

Concretely, this is option B now and option D (or C) as the transport, chosen after bd-78t:

1. Make issues explicitly global: move the DB to `$(git rev-parse --git-common-dir)/beads/` so every worktree sees the same file by construction. That removes redirect files, `bd worktree create` special-casing, and most worktree detection.
2. Clean the storage seam: remove the 27 concrete-type assertions, make the importer accept `storage.Storage`, and move dirty tracking/export hashes out of the core interface into the transport layer.
3. Replace JSONL-in-tree sync with one transport interface (`Push`, `Pull`, per-issue last-write-wins by `updated_at`, union for labels/deps, tombstones for deletes). Keep the simpler `sync_merge.go` rules and drop `internal/merge` and its merge driver.
4. Implement the git-ref transport as the default. GitHub Issues (and others from bd-78t) become alternative transports behind the same interface.

Steps 1–2 are no-regret: they are needed whatever bd-78t concludes. Step 3's interface is effectively the plugin seam bd-78t is evaluating, so the two should share a design.

### Expected removals

Once the transport replaces in-tree JSONL, these become candidates for deletion (to be confirmed per item):

- Git hooks and their installers (`cmd/bd/hooks.go`, `init_git_hooks.go`, templates), merge driver (`cmd/bd/merge.go`, `internal/merge`).
- Sync branch (`internal/syncbranch`, `cmd/bd/sync_branch.go`) and its hidden worktree.
- Auto-flush / auto-import / staleness checks (`cmd/bd/autoflush.go`, `flush_manager.go`, `internal/autoimport`), snapshots (`snapshot_manager.go`, `deletion_tracking.go`).
- Redirect handling and worktree workarounds (`cmd/bd/worktree.go`, much of `worktree_cmd.go`).
- Resurrection/orphan policies, if per-issue LWW with tombstones proves sufficient.
- Most of the daemon's reason to exist (feeds bd-fxy).

Rough estimate: 12–15K LOC, before counting tests.

## Migration sketch

1. Ship steps 1–2 with the current JSONL sync still working. The DB moves; on first run, bd imports the existing `.beads/issues.jsonl` if the new location is empty.
2. Add the transport interface and git-ref transport behind a config flag. `bd sync` pushes/pulls the ref; a one-time command seeds the ref from the current DB.
3. Once it has been stable across machines for a while, make it the default, stop exporting to `.beads/issues.jsonl`, leave a final copy in the tree with a README pointing at the ref, and delete the old machinery.

## Open questions

- Should a project ever want branch-scoped issues? This recommendation says no, matching current actual behavior.
- Should `bd` keep working outside a git repo? A git-ref transport needs a repo; plain local SQLite does not.
- Daemon: with no auto-flush/import loop, is there anything left for it to do? (bd-fxy)
