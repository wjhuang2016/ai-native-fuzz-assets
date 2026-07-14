# AI Native Fuzz Assets

This repository is a curated asset base for TiDB-oriented AI-native bug mining.

It treats three things as first-class reusable assets:

1. Bug assets: concrete bug drafts, compact method cases, evidence logs, and bug-db state.
2. Methodology assets: the loop, proof-obligation framing, selector/root-cause/oracle ledgers, and handoff notes.
3. Exploration scaffolds: probe programs, harness scripts, and TiDB-side AI-native test fixtures.

## Snapshot

- `docs/core/`: 19 core methodology and ledger documents.
- `docs/handoff/`: latest handoff snapshot for session-to-session continuation.
- `docs/bug-index/`: curated and generated high-severity bug indexes.
- `docs/github-issues/`: filed GitHub issue bodies for important bugs, including #69845.
- `docs/bug-drafts/`: 126 draft bug writeups and filed-issue source bodies.
- `docs/method-cases/`: 115 compact case summaries tied to bug-db entries.
- `assets/store/`: JSONL/SQLite/log evidence store and seed/selector assets.
- `assets/bug-db/`: local bug-db sync helpers such as pending SQL.
- `scaffolds/top-level/`: 63 top-level Python/shell/data scaffolds from the working directory.
- `scaffolds/go-probes/`: 27 Go probe/source-scanner files plus `go.mod` and `go.sum`.
- `scaffolds/client-go-tests/`: reusable client-go integration probes and counterfactual patches.
- `scaffolds/tidb-tests/`: 69 TiDB-side AI-native test/probe and counterfactual fixtures.
- `scripts/`: asset refresh and bug-index generation helpers.
- `tools/txnlab/`: pinned cross-layer transaction experiment runner, strong oracles, fault control,
  evidence capture, automatic cleanup, and asset-store writeback.

Current tracked snapshot size is about 25 MB.

## Layout

`docs/`

- `handoff/`: the running handoff document.
- `bug-index/`: high-severity bug index for prioritizing future mining.
- `github-issues/`: issue bodies that were filed upstream.
- `core/`: the main loop, methodology, ledgers, oracle libraries, and supporting notes.
- `bug-drafts/`: narrative bug writeups with evidence and reasoning.
- `method-cases/`: compact reusable case cards organized by bug id.

`assets/`

- `store/`: the reusable data plane for the methodology, including logs, result JSONL, SQLite state, and seed inventories.
- `bug-db/`: local bug-db helper material.

`scaffolds/`

- `top-level/`: Python and shell probes copied from the original workspace root.
- `go-probes/`: standalone Go probes and current-source scanners used against live clusters or
  focused proof-obligation shapes. `failed_publication_owner_scan.go` finds failed-publication
  owner resets; `nonblocking_semantic_send_scan.go` inventories default-droppable channel sends;
  `deferred_terminal_error_scan.go` inventories fallible deferred terminal actions.
- `tidb-tests/`: TiDB-repo-side AI-native harness tests and probe fixtures.
- `client-go-tests/`: client-go integration probes plus minimal counterfactual patches used to
  distinguish a terminal-contract RED from an expected pre-proof rejection.

`scripts/`

- `sync_assets.sh`: incrementally refreshes this repository from `/Users/bba/pc` or another `SOURCE_ROOT`.
- `refresh_severe_bug_index.sh`: regenerates `docs/bug-index/SEVERE_BUGS_FROM_DB.md` from the remote `found_bug` table.

`tools/txnlab/`

- `README.md`: transaction experiment contract, safety gates, commands, and verified environment.
- `runner.py`: prepare/arm/workload/observe/cleanup execution with evidence-first failure handling.
- `oracles.py`: executable O56/O57/O58 transaction truth oracles.
- `examples/`: exact testbed 8220955 pins, inert fault templates, Chaos example, and oracle inputs.
- `local.py`: refreshed-nightly capability checks plus exact-SHA local TiKV build, verification, and
  self-cleaning realtikvtest execution.

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
