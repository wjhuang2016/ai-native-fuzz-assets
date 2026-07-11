# AI Native Fuzz Assets

This repository is a curated asset base for TiDB-oriented AI-native bug mining.

It treats three things as first-class reusable assets:

1. Bug assets: concrete bug drafts, compact method cases, evidence logs, and bug-db state.
2. Methodology assets: the loop, proof-obligation framing, selector/root-cause/oracle ledgers, and handoff notes.
3. Exploration scaffolds: probe programs, harness scripts, and TiDB-side AI-native test fixtures.

## Snapshot

- `docs/core/`: 16 core methodology and ledger documents.
- `docs/handoff/`: latest handoff snapshot for session-to-session continuation.
- `docs/bug-index/`: curated and generated high-severity bug indexes.
- `docs/bug-drafts/`: 82 draft bug writeups.
- `docs/method-cases/`: 64 compact case summaries tied to bug-db entries.
- `assets/store/`: JSONL/SQLite/log evidence store and seed/selector assets.
- `assets/bug-db/`: local bug-db sync helpers such as pending SQL.
- `scaffolds/top-level/`: 60 top-level Python/shell/data scaffolds from the working directory.
- `scaffolds/go-probes/`: 19 Go probe source files plus `go.mod` and `go.sum`.
- `scaffolds/tidb-tests/`: 11 TiDB-side AI-native test/probe files used as harness fixtures.
- `scripts/`: asset refresh and bug-index generation helpers.

Current snapshot size is about 17 MB.

## Layout

`docs/`

- `handoff/`: the running handoff document.
- `bug-index/`: high-severity bug index for prioritizing future mining.
- `core/`: the main loop, methodology, ledgers, oracle libraries, and supporting notes.
- `bug-drafts/`: narrative bug writeups with evidence and reasoning.
- `method-cases/`: compact reusable case cards organized by bug id.

`assets/`

- `store/`: the reusable data plane for the methodology, including logs, result JSONL, SQLite state, and seed inventories.
- `bug-db/`: local bug-db helper material.

`scaffolds/`

- `top-level/`: Python and shell probes copied from the original workspace root.
- `go-probes/`: standalone Go probes used against live clusters or focused scenarios.
- `tidb-tests/`: TiDB-repo-side AI-native harness tests and probe fixtures.

`scripts/`

- `sync_assets.sh`: incrementally refreshes this repository from `/Users/bba/pc` or another `SOURCE_ROOT`.
- `refresh_severe_bug_index.sh`: regenerates `docs/bug-index/SEVERE_BUGS_FROM_DB.md` from the remote `found_bug` table.

## Suggested Reading Order

1. `docs/handoff/ai-native-fuzz-handoff.md`
2. `docs/core/START_HERE.md`
3. `docs/bug-index/SEVERE_BUGS.md`
4. `docs/core/ai-native-autonomous-loop.md`
5. `docs/core/ai-native-proof-obligation-methodology-v2.md`
6. `docs/core/ai-native-selector-ledger.md`
7. `docs/core/ai-native-root-cause-ledger.md`

## Refresh Workflow

From the repository root:

```bash
scripts/sync_assets.sh --refresh-bug-index
```

To refresh, commit, and push in one step:

```bash
scripts/sync_assets.sh --refresh-bug-index --push
```

## Deliberate Omissions

- Compiled probe binaries were not copied.
- The full TiDB worktree was not mirrored here.
- Generated failpoint binding files were not mirrored here.

The goal is to keep this repo as a portable asset base rather than a second full source checkout.
