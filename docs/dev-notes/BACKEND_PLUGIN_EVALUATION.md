# Pluggable Backend Evaluation (bd-78t)

Status: first dig, 2026-10-19. Evaluated at v0.48.1. Related: bd-1y5 (storage, blocked on this), bd-eo4 (Gastown removal), [STORAGE_EVALUATION.md](STORAGE_EVALUATION.md).

## Headline

"Backend" means two different things, and they need two different seams:

- **Store**: the source of truth, on this machine, that bd queries. It must answer "what's ready?" quickly. Candidates: SQLite, markdown files (e.g. an Obsidian vault) indexed into SQLite.
- **Bridge**: a remote tracker that bd syncs with. Candidates: GitHub Issues, Jira, Linear, Flow Production Tracking (ShotGrid), Perforce jobs, GitLab.

No remote tracker can compute bd's ready-work query server-side (open, no open blockers, transitively through parents). GitHub's `is:blocked` search is not transitive; Jira's JQL can't filter on blocker status without add-ons; the others have no equivalent. Every remote option therefore ends up as a bridge in front of a local store, which is what the existing Linear and Jira integrations already do. git-bug, the closest prior art, uses the same split (local git storage plus "bridges" to GitHub/GitLab/Jira).

## What the core actually needs

### Methods

The current `storage.Storage` interface has 62 methods, and code goes around it in 19 places by casting to `*sqlite.SQLiteStorage`. Core tracking commands (create, update, show, list, ready, blocked, close, reopen, dep, label, comments, search, stats, epic, defer, stale) use about 25 of them. The rest serve sync/import/export (dirty tracking, export hashes, metadata), prefix renames, or Gastown/molecule features (`RunInTransaction`, `GetMoleculeProgress`, `GetIssuesByLabel`). Eight methods have no callers outside the storage implementations at all.

Of the 25, about half are graph queries: `GetReadyWork`, `GetBlockedIssues`, `IsBlocked`, `GetNewlyUnblockedByClose`, `GetEpicsEligibleForClosure`, `GetDependencyTree`, `DetectCycles`, `GetStatistics`, `GetStaleIssues`, `GetDependencyCounts`. These don't need to be per-backend. They can be computed in one shared Go package from "all issues + all dependencies", with SQLite optionally providing a fast path.

### Fields

`types.Issue` has 65 fields: about 22 core tracking, 14 sync/storage bookkeeping, and 29 Gastown/agent/molecule/HOP fields (agent state, rig, gates, slots, formulas, events, quality score, and more). `Dependency` carries two more Gastown fields (`Metadata` for waits-for gates, `ThreadID`). There are 19 well-known dependency types, of which about 6 matter outside Gastown (`blocks`, `parent-child`, `related`, `discovered-from`, `duplicates`, `supersedes`). There are 8 built-in statuses, including Gastown's `pinned` and `hooked`.

Every backend would have to map or carry all of that. Removing Gastown first (bd-eo4) roughly halves what a backend must support.

### IDs

Top-level and child IDs are allocated by the store (hash IDs sized by row count, `child_counters` table), but the hash function itself is pure and client-side. Linear and Jira already keep local IDs and record the remote identity in `external_ref` (a URL, unique-indexed). That model fits every bridge. Native remote IDs as bd IDs (e.g. `gh-123`) would also work syntactically, but partial-ID resolution does a substring search over all issues, which is slow remotely and ambiguous with numeric IDs.

## Proposed interfaces (sketch)

```go
// Store is the source of truth bd queries. Small on purpose.
type Store interface {
    GetIssue(ctx, id) (*Issue, error)
    ListIssues(ctx, Filter) ([]*Issue, error)
    CreateIssue(ctx, *Issue) error          // assigns ID if empty
    UpdateIssue(ctx, id string, IssuePatch) error  // typed patch, not map[string]any
    CloseIssue(ctx, id, reason string) error
    DeleteIssue(ctx, id string) error

    AddDependency(ctx, Dependency) error
    RemoveDependency(ctx, from, to string) error
    ListDependencies(ctx) ([]Dependency, error)

    AddLabel(ctx, id, label string) error
    RemoveLabel(ctx, id, label string) error
    AddComment(ctx, id string, Comment) error
    ListComments(ctx, id string) ([]Comment, error)

    GetConfig(ctx, key string) (string, error)
    SetConfig(ctx, key, value string) error
    Close() error
}

// Optional fast path; the shared graph package is used when absent.
type ReadyQuerier interface {
    GetReadyWork(ctx, WorkFilter) ([]*Issue, error)
    GetBlockedIssues(ctx, WorkFilter) ([]*BlockedIssue, error)
}

// Bridge syncs a Store with a remote tracker.
type Bridge interface {
    Name() string
    Pull(ctx, store Store, since time.Time) (PullResult, error)
    Push(ctx, store Store, ids []string) (PushResult, error)
}
```

Notes:

- A typed `IssuePatch` replaces `UpdateIssue(map[string]interface{})`. The untyped map appears to have hidden at least one bug (see Findings).
- The graph package would become the single definition of "blocked". Today there are three that disagree (see Findings).
- The existing prefix router (`internal/routing`, `cmd/bd/routed.go`) already dispatches IDs to different stores by prefix. It may be reusable, but it is not needed for a first version.
- `beads.go` exports today's `Storage` interface publicly, and VC depends on it. Add the narrow interface alongside it rather than breaking it, then decide separately whether to keep the public API.

## Candidates

| Candidate | Role | Fit | Gaps / costs | Verdict |
|---|---|---|---|---|
| SQLite | Store | Everything native; recursive CTE ready-work in ms; offline | Needs a sync story across machines | Keep as the default store |
| Markdown files (Obsidian vault) | Store, with SQLite as a rebuildable index | Human-readable and editable; no lock-in; vault sync handles multi-machine | Properties can't nest, so deps are flat lists (`blocked_by: [bd-12]`); Bases can't express transitive queries (index needed); Obsidian Sync merges markdown with diff-match-patch, which can corrupt YAML on conflict; Syncthing/iCloud leave conflict copies to detect | Feasible; strongest "store" alternative |
| GitHub Issues | Bridge | Blocking deps GA (Aug 2025, 50 per type), sub-issues native (100 per parent, 8 deep), issue types, labels; easy auth; complete export | Only open/closed state (in_progress, blocked emulated via labels or fields); no related/discovered-from; 500 creates/hour cap; no offline | Best bridge candidate |
| Jira Cloud | Bridge | Best data-model fit: workflows, typed links, sub-tasks, custom fields | Heavy auth and config; JQL can't do ready-work; current integration shells out to Python and drops comments and links on push | Port only if needed |
| Linear | Bridge | Clean fit; existing Go integration | SaaS only; priority scale offset; existing push drops labels, deps, assignee, comments | Existing code is a reference; keep or drop |
| GitLab | Bridge | Self-hostable; labels, links | Blocking links need Premium or Ultimate; Free has only `relates_to` | Only for Premium users |
| Flow Production Tracking (ShotGrid) | Bridge | Ticket entity, configurable statuses and fields, Python and REST APIs | Typed deps need custom fields; per-seat cost; new self-hosted installs no longer offered | Only if a studio already uses it |
| Perforce jobs | Bridge | Customizable jobspec; `p4 fix` links jobs to changelists | No job-to-job links, no comments (emulated as text fields); needs a server | Only in a Perforce shop |
| git-bug and similar | Prior art | Git refs as storage, bridges to GitHub/GitLab/Jira | No relations or custom fields | Study its bridge design; don't adopt |

Newer agent-oriented tools (grite, git-issues, jira-nano, tbd) have converged on "git or markdown as truth, SQLite as cache", which is close to bd's pre-Dolt design. All are pre-1.0.

## Recommendation

Pursue it, scoped to two small seams and few implementations:

1. Do bd-eo4 (Gastown removal) first. It removes about 29 issue fields, 13 dependency types, 2 statuses and several methods a backend would otherwise have to carry.
2. Define `Store` plus the shared graph package, and move SQLite behind it. Keep the memory store as the test double; it already exists for `--no-db`.
3. Define `Bridge`. Build only what is actually used: likely one bridge (GitHub) to prove the shape. Rework Linear and Jira to fit it, or remove them.
4. Treat ShotGrid, Perforce, GitLab and Jira as "possible later" bridges. Don't build speculatively.

Whether a second store (markdown/Obsidian) is worth building is the open question for bd-1y5.

## Findings worth fixing regardless

- **Three definitions of "blocked" disagree.** `bd ready` (via the blocked cache) honors `blocks`, `conditional-blocks`, `waits-for` and `parent-child`. `bd blocked` (`GetBlockedIssues`, `internal/storage/sqlite/ready.go:517`) and `IsBlocked` (`ready.go:708`) only look at `blocks`. The memory store only looks at `blocks` (`internal/storage/memory/memory.go:1203`). So an issue blocked through its parent is hidden from `bd ready` and also missing from `bd blocked`.
- **`conditional-blocks` omits `hooked`** from its active-blocker statuses (`blocked_cache.go:163` vs `:149`). This looks unintentional, and goes away with bd-eo4.
- **Linear conflict reimport appears to always fail.** `BuildLinearToLocalUpdates` puts `labels` and `updated_at` in the update map (`internal/linear/mapping.go:528,534`); neither is in the allowed-field list, and `UpdateIssue` rejects unknown keys (`internal/storage/sqlite/queries.go:831`). Not reproduced.
- **`GetNextChildID` runs outside the create transaction** (`cmd/bd/create.go:352`), so concurrent child creation could race. Low risk for a solo user.
- **`storage.Config.Backend` ("sqlite"/"postgres") has no callers.** It is dead config.
