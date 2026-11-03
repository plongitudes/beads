# Rewrite Hand-off

Written 2026-11-02 from plongitudes/beads at v0.48.1 (commit 7f0ff622b, `git@github.com:plongitudes/beads.git`). Purpose: give the new repo everything decided so far, so the design discussion starts from here instead of re-deriving it. Old-repo paths below refer to that commit.

Background reading in the old repo: `docs/dev-notes/STORAGE_EVALUATION.md` (bd-1y5), `docs/dev-notes/BACKEND_PLUGIN_EVALUATION.md` (bd-78t).

## Why a rewrite

About half of the old codebase's ~119K non-test Go lines is machinery the new shape doesn't need: Gastown features (~18K), JSONL/git sync (~16–20K), daemon and RPC (~13K), and `bd doctor` (~11.6K, mostly diagnosing sync, daemon, and worktree problems). Nearly every bug found during evaluation lives in those areas. Refactoring incrementally would mean keeping that machinery working while changing it; a rewrite carries over the sound half and drops the rest.

## Decisions

| Decision | Why |
|---|---|
| Core in Go | Single static binary, fast startup (agents call it often), pure-Go SQLite, and most carry-over code ports directly. Bridges may be other languages (see open questions). |
| No Dolt, no Gastown | The fork exists to stay simple. Dolt is too heavy for this use; Gastown's multi-agent orchestration is the opposite of the intended workflow. |
| Issues are global, not branch-scoped | That is how the old tool actually behaved; storing global data in branch-tracked files caused most of its sync and worktree bugs. |
| SQLite is the local store and query engine | Recursive queries make ready/blocked fast; works offline; no server. |
| The DB lives in git's shared directory (`git rev-parse --git-common-dir`) | Every worktree sees the same DB by construction. No redirect files, no worktree detection. |
| Default sync transport: a git ref that is never checked out (e.g. `refs/beads/*`) | Works with any git remote, so no host lock-in; works in cloud sessions; never touches code branches. |
| Remote trackers are bridges, not stores | No remote tracker (GitHub, Jira, Linear, ShotGrid, Perforce, GitLab) can compute ready-work server-side, so each needs a local store in front of it anyway. |
| v1 scope: one store (SQLite), one transport (git ref), plus in-memory adapters for tests | "One adapter means a hypothetical seam; two means a real one." Leave seams where a second backend would go, but don't build backends until they're wanted. |
| One definition of "blocked" | The old tool had three that disagreed (see Lessons). |
| Typed updates, not `map[string]any` | The untyped update map in the old tool hid at least one bug. |

## Target users

Solo developer, several machines (including remote/cloud Claude sessions), git worktrees with parallel agents. Open to small-team use later.

## Carry over

### Data model

Issue fields (22 of the old 65):

- Minimum: `ID`, `Title`, `Description`, `Status`, `Priority` (0–4, 0 highest), `IssueType`, `Assignee`, `CreatedAt`, `UpdatedAt`, `ClosedAt`, `Labels`, `Dependencies`, `Comments`.
- Extended: `Design`, `AcceptanceCriteria`, `Notes`, `EstimatedMinutes`, `CreatedBy`, `Owner`, `CloseReason`, `DueAt`, `DeferUntil`.
- Consider keeping `ExternalRef` (remote identity for bridges; old tool unique-indexed it).

Old definition: `internal/types/types.go:14-125`.

Statuses: `open`, `in_progress`, `blocked`, `deferred`, `closed`. Drop `pinned` and `hooked` (Gastown). Old tool's tombstone status was a sync artifact; decide fresh whether deletes need one (see open questions).

Dependency types: `blocks`, `parent-child`, `related`, `discovered-from`, `duplicates`, `supersedes`. Only `blocks` and `parent-child` affect readiness. Drop `conditional-blocks`, `waits-for`, and the 11 HOP/agent types.

Comments: id, issue id, author, text, created at.

### Behavior worth keeping

- `ready` = status `open` or `in_progress`, no active blocker, not deferred into the future, checked transitively through parents. Active blocker = `blocks` target whose status is not `closed`; a blocked parent blocks its descendants.
- Hash IDs: `<prefix>-<base36 hash>`, length grows with issue count; child IDs `<parent>.<n>`. Old code: `internal/idgen/hash.go`, `internal/storage/sqlite/adaptive_length.go`, `hash_ids.go`.
- Partial ID resolution (exact, then prefix, then unique substring) with ambiguity errors: `internal/utils/id_parser.go`. Make the substring step an indexed query, not a full scan.
- CLI command names, flags, and `--json` output shapes, so existing agent instructions keep working: `create`, `update`, `show`, `list`, `ready`, `blocked`, `close`, `reopen`, `dep add/remove/tree/cycles`, `label`, `comments`, `search`, `stats`, `defer`.
- Visual style: small Unicode status symbols (`○ ◐ ● ✓ ❄`) with semantic colors, no emoji. Old `internal/ui`.

### Code worth porting or reading

- SQLite schema and recursive queries: `internal/storage/sqlite/schema.go`, `ready.go`, `dependencies.go`, `blocked_cache.go` (the materialized blocked cache took ready-work from 752ms to 29ms on 10K issues).
- `internal/idgen`.
- `internal/timeparsing` (`+1d`, `next monday`, etc.) and `internal/validation`.
- Pure-Go SQLite driver choice: `ncruces/go-sqlite3`, WAL mode, `BEGIN IMMEDIATE` with busy retry.
- Merge rules from `cmd/bd/sync_merge.go:66-237`: scalars last-write-wins by `updated_at`, labels and dependencies unioned, comments appended and deduplicated.
- Behavioral spec: `cmd/bd/testdata/*.txt`, 17 testscript files (create, ready, blocked, close, dep add/remove/tree, list, show, update, import, export, stats, init, help, quickstart, version). Use as acceptance tests for the spike.

### Migration

The new tool should import the old `.beads/issues.jsonl` (one JSON object per line) so current issues come along.

## Leave behind

Daemon and RPC; git hooks and the merge driver; sync branch and its hidden worktree; auto-flush, auto-import, and dirty tracking; snapshots and base files; resurrection and orphan policies; redirect files and worktree workarounds; routing and multi-repo hydration; molecules, formulas, gates, slots, agents, swarms, and other Gastown features; compaction; most of `bd doctor`; the `UnderlyingDB()` extension point.

## Lessons from the old codebase

Bugs found during evaluation (not all reproduced). Each points to a design rule.

- **Three definitions of "blocked."** `bd ready` honored four dependency types; `bd blocked`, the close guard, and "newly unblocked" honored only `blocks`; the memory store also only `blocks`. Rule: one blockedness module, every query derives from it.
- **Snapshot rules smeared over ~20 sites.** 9 writers and 11 readers of `issues.jsonl` drifted: some exports leaked ephemeral issues, `bd import` failed on lines over 64KB that auto-import accepted. Rule: one module owns the snapshot format and policy; storage location is an adapter.
- **Every command written twice** (daemon and direct paths), and the copies diverged. Rule: one code path.
- **Caller-side dirty flag.** Several commands never set it, so changes synced late. Rule: the store tracks its own changes.
- **Untyped update map.** The Linear conflict path sent keys the store rejected. Rule: typed patches.
- **Three ID schemes** (normal, migration, memory store). Rule: one generator.
- **Child ID allocated outside the create transaction.** Rule: allocate inside it.
- **Redirect path resolved from the wrong directory.** Moot without redirects.
- **Cycle check counts `related` links**, so a `related` link blocked adding a real `blocks` dependency. Rule: only readiness-affecting types participate in cycle detection.
- **Two merge engines, two base files.** Rule: one pure merge function.
- **46 files shelled out to git directly.** Rule: one small git module, sized for what the transport needs, with a fake for tests.
- **Package globals** made every function's real interface implicit and tests fragile. Rule: pass dependencies in.

## Open questions for the design session

- **Bridge mechanism.** Idea: bridges as separate executables (`bd-bridge-<name>` on `PATH`, JSON over stdin/stdout, like `git-remote-<name>`). Keeps the core small, lets a ShotGrid or Perforce bridge use their Python SDKs, and isolates crashes.
- **Second store.** Markdown files (e.g. an Obsidian vault) as the source of truth with SQLite as a rebuildable index. Worth designing the store seam with this in mind; not v1.
- **Git-ref layout.** One file per issue (small conflicts, clear history) vs one JSONL file. How deletes travel (tombstones and their TTL).
- **Pull semantics.** Merge on every command, or only on explicit `sync`? What happens offline?
- **Agent context integration.** How the tool keeps itself in the agent's context with modern Claude Code (skills, hooks, a plugin) without the forceful, outdated instructions the old `bd prime`/`bd setup` emitted. Old bead bd-ooz has the inventory task.
- **Events/audit trail.** Old tool wrote an events table nothing read. Keep or drop?
- **CLI compatibility.** Exact compatibility with old `bd` flags and JSON, or compatible in spirit?
- **Name and binary.** Same `bd` name, or a new one to avoid confusion while both exist?

## Suggested first spike

1. Types, SQLite store, blockedness module, ID generation.
2. Commands: `create`, `update`, `show`, `list`, `ready`, `blocked`, `close`, `dep`.
3. Run the old testscript files against it.
4. Import the old `issues.jsonl`.

If it passes in a few sessions, commit to the rewrite. If it keeps hitting things the old code handled for good reasons, revisit.
