# Clone State Candidate Scanner

`clone-state-scan` is a source-only candidate generator for replay-persistent mutable state. It uses
Go's parser to find mutable-looking struct fields that are copied by a `Clone` method and then used
by `eval*` or `vecEval*` methods.

Run it against a pinned TiDB worktree:

```bash
go run tools/clone-state-scan/main.go \
  --root .txnlab/worktrees/campaign.txn.terminal.20260714/tidb \
  pkg/expression
```

The JSON output is not a bug verdict. Every row still needs proof of mutation before the replay
edge, reuse after the edge, a supported execution route, and a correctness-bearing consumer. On the
current source pin, the expression scan returns only `builtinRandSig`; that root is already terminal.

