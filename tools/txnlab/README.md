# Transaction Experiment Control Plane

`txnlab` turns a transaction proof obligation into a pinned, auditable experiment. It is a quality
assurance harness for authorized local environments and testbeds. It is not a generic chaos tool and
does not choose a target by itself.

## Ready Surface

- Exact source pins and isolated worktrees for TiDB, client-go, and TiKV.
- Exact-SHA local TiKV build/verification and a self-cleaning `realtikvtest` runner.
- Explicit refreshed-nightly mode that records the downloaded TiKV commit and limits the verdict to
  capability/current-nightly coverage when it differs from the source pin.
- Registry checks for exact TiDB/TiKV failpoint images, including OCI revision-label verification.
- Read-only TidbCluster health, replica, RBAC, service, and pod snapshots.
- Explicitly gated image switching, TiDB/TiKV HTTP failpoints, SQL workloads, and Chaos Mesh objects.
- Structured phase/action events, command output, before/after snapshots, component logs, hashes,
  and a manifest in one evidence bundle. Logs are captured both before and after cleanup so an image
  restoration restart cannot erase the fault window.
- Automatic failpoint removal, run-labeled Chaos deletion, and image restoration in `finally`.
- Executable O56, O57, and O58 oracles with `RED`, `GREEN`, and `INVALID` outcomes.
- JSONL run records compatible with `assets/store/store.py`; RED creates a promotion candidate but
  never creates a confirmed bug automatically.
- Official `PingCAP-QE/artifacts` failpoint build-script generation for a future source hook.
- Packet-only AI source scouting with hard input-size, source-region, candidate-count, and
  wall-clock limits. It does not hard-code a model and never uses `--output-schema`.

The pinned 8220955 preparation has been live-checked against one TiDB, three TiKV, and one PD
replica. The cluster is Ready, required RBAC checks pass, both exact failpoint images exist, and the
three source commits have isolated worktrees under `.txnlab/worktrees/`.

## Commands

Run from the repository root:

```bash
python3 -m tools.txnlab validate tools/txnlab/examples/testbed-8220955.toml
python3 -m tools.txnlab preflight tools/txnlab/examples/testbed-8220955.toml
python3 -m tools.txnlab prepare-worktrees tools/txnlab/examples/testbed-8220955.toml
```

Refresh a stale TiUP TiKV nightly, then run a capability baseline and a target matrix:

```bash
python3 -m tools.txnlab refresh-nightly tikv
python3 -m tools.txnlab realtikvtest tools/txnlab/examples/testbed-8220955.toml \
  TestSharedLockBlockExclusiveLock --nightly
python3 -m tools.txnlab realtikvtest tools/txnlab/examples/testbed-8220955.toml \
  TestSharedLockReleaseOneHolderDoesNotAllowParentDelete --nightly
```

Nightly is a fast capability lane, not an exact-source oracle. For a revision-pinned L2 result:

```bash
python3 -m tools.txnlab local-build tools/txnlab/examples/testbed-8220955.toml tikv
python3 -m tools.txnlab local-verify tools/txnlab/examples/testbed-8220955.toml tikv
python3 -m tools.txnlab realtikvtest tools/txnlab/examples/testbed-8220955.toml \
  TestName --timeout 300
```

The runner refuses to reuse port 2379, verifies exact binaries before startup, records both logs,
and terminates only the playground process group it created. Always run a feature-specific baseline
before interpreting a candidate result; a cached `nightly` alias can point to a months-old binary.

Run a bounded source scout only after the main loop has selected the relevant owners. The child
agent receives the numbered snippets inline and has no repository path in its working directory:

```bash
python3 -m tools.txnlab source-packet-scout \
  tools/txnlab/examples/testbed-8220955.toml \
  --question 'Can the exclusive upper bound omit a flushed lock from terminal resolve?' \
  --region client-go:txnkv/transaction/txn.go:640:675 \
  --region client-go:txnkv/transaction/pipelined_flush.go:301:365 \
  --region client-go:txnkv/transaction/pipelined_flush.go:458:525 \
  --region client-go:txnkv/rangetask/range_task.go:156:245 \
  --timeout 90
```

The input limits are enforced before launch: at most 12 regions, 240 lines per region, 1,200 lines
and 32 KiB total. The outer process kills the complete scout process group at the wall-clock limit.
Prompt-only command/token budgets are intentionally not treated as controls; the main loop must
curate the source packet first.

The 32 KiB cap is calibrated, not decorative. A 47 KiB packet timed out at 75 seconds, while a
25 KiB packet completed in about 45 seconds. Reported token usage remains observational because the
CLI does not expose a hard output-token limit in this provider.

Evaluate an oracle without touching a cluster:

```bash
python3 -m tools.txnlab oracle terminal_mvcc_truth \
  tools/txnlab/examples/oracle-inputs/o56-red-failure-after-commit.json
```

Generate official failpoint image build scripts after a source hook is admitted:

```bash
tools/txnlab/bootstrap.sh
python3 -m tools.txnlab render-build tools/txnlab/examples/testbed-8220955.toml tidb
python3 -m tools.txnlab render-build tools/txnlab/examples/testbed-8220955.toml tikv
```

The generated script uses `packages.yaml.tmpl`, exact Git SHA, Linux/amd64, and the `failpoint`
profile. Building and pushing it still requires PingCAP's image-builder/kaniko environment and
registry credentials. Existing pinned failpoint images do not require that path.

## Mutation Gate

A testbed experiment with SQL, image, failpoint, or Chaos actions needs both:

```toml
allow_mutation = true
```

and:

```bash
python3 -m tools.txnlab run EXPERIMENT.toml --allow-mutation
```

An action is also inert when `enabled = false`. The checked-in 8220955 template leaves every
mutating action disabled. Enabling it is legal only after the target has a complete P/Q/F card,
one-shot selector, required boundary witness, and highest-consumer oracle input producer.

Emergency cleanup removes failpoints named by the config and all Chaos objects carrying the run
label:

```bash
python3 -m tools.txnlab cleanup EXPERIMENT.toml
```

Image restoration is normally handled from the in-memory pre-run snapshot. Do not use emergency
cleanup as a substitute for retaining the original run evidence.

## Experiment Contract

The TOML phases are fixed:

1. `prepare`: setup, pinned image switch, workload data.
2. `arm`: one selected fault altitude.
3. `workload`: one transaction/key set and its controls.
4. `observe`: boundary witness and durable-state extraction.
5. `cleanup`: explicit cleanup, followed by automatic cleanup.

Supported action kinds are `command`, `sql`, `failpoint_arm`, `failpoint_disarm`, `chaos_apply`,
`chaos_delete`, `image_switch`, `wait_log`, `collect`, and `sleep`. Commands are argv arrays rather
than shell strings. On a testbed, commands are considered mutating unless they explicitly declare
`read_only = true`. Every failpoint action must name its pod or pods, and Chaos selectors are
confined to the configured namespace. Workload helpers receive:

- `TXNLAB_RUN_KEY`
- `TXNLAB_RUN_DIR`
- `TXNLAB_WORKTREE_TIDB`
- `TXNLAB_WORKTREE_CLIENT_GO`
- `TXNLAB_WORKTREE_TIKV`
- `KUBECONFIG` and `TXNLAB_NAMESPACE` for testbed runs

The workload or observer writes the configured oracle JSON into `TXNLAB_RUN_DIR`. Final database
state alone is insufficient: O56 requires apply altitude, O57 requires new-owner-before-cleanup
ordering, and O58 requires a multi-region accepted prefix plus fallback witness.

## Evidence And Assets

Each run produces:

```text
runs/<run-key>/
  config-summary.json
  preflight.json
  events.jsonl
  actions/*.json
  snapshots/{before,after}.json
  logs/{pre-cleanup,post-cleanup}/*.log
  oracle-{input,result}.json
  cleanup.json
  run-record.jsonl
  promotion-candidate.json   # RED only
  manifest.json
  evidence-index.json
```

Set `assets.auto_import = true` only when the obligation and every `used_assets` key already exist
in the selected SQLite store. An import failure is recorded in the bundle but does not rewrite the
experimental verdict.

## Remaining Target-Specific Work

The environment is ready with two explicit scopes: refreshed nightly for fast capability screening,
and a source-pin build for exact L2 reproduction. The next work is scientific:

- Complete one current-source P/Q/F card and admit one target only.
- Add the smallest transaction-scoped selector if existing one-shot failpoints are too broad.
- Write the workload/evidence extractor that emits O56, O57, or O58 input.
- Run L1/L2 first. The campaign's testbed gate remains closed until a local RED and exact owner
  attribution exist.

See `failpoint-inventory.md` for available boundaries and their selector limits.
