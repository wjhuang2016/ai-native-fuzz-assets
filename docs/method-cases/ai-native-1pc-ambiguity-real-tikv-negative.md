# 1PC lost-response fallback: real-TiKV negative case

## Candidate

The current client-go path looked like a strong terminal-truth candidate:

```text
P: a TryOnePc request may have committed, but its response is lost
Q: after EpochNotMatch regrouping disables 1PC, the final retry result can be returned normally
F: the caller could receive an ordinary error although the transaction committed
```

The compressed schedule sent a real successful TryOnePc request, replaced only its response with an
RPC error, paused the retry, split the key range, and then let the same transaction retry across the
new Regions.

## Provisional local RED

The embedded mock/unistore path returned an ordinary write conflict while both keys were already
visible. At that point `IsOnePC=false`, `undeterminedErr=nil`, and recursive regrouping had also set
the global prewrite-cancel flag. A request-scoped counterfactual that retained the earlier fast-commit
RPC ambiguity converted the result to explicit undetermined and made the safe oracle GREEN.

This was useful locator evidence, but not yet a product RED. The mock returned the second 1PC
prewrite as an ordinary write conflict.

## Dominating real owner

Real TiKV has a stronger retry contract. Its prewrite command calls
`check_committed_record_on_err`; when a repeated 1PC prewrite encounters the transaction's own
committed record, TiKV returns the previous `one_pc_commit_ts` and applies no new modifications.
The client retry therefore completed successfully after the real EpochNotMatch split. Both values
were visible, `commitTS > startTS`, and the probe then deleted its dedicated keys.

The candidate is retired. It is a semantic gap between the embedded local store and real TiKV, not
a client-go/TiKV product bug, and it was not inserted into the bug database.

## Harness lesson

The first real lift tried to pause retry and split the Region from the same process. The process-wide
failpoint also paused the splitter's request, producing a harness deadlock. The valid run used a
second process as the topology actor and file markers only for ordering.

## Reusable rule

For a cross-layer retry candidate, local RED is provisional until the downstream retry owner is
closed:

```text
local terminal mismatch
  -> exact real request replay semantics
  -> idempotency/committed-record owner
  -> final terminal and durable oracle
```

Promote only when the contradiction survives the real downstream owner. Keep process-local
failpoint ownership separate from independent topology or recovery actors.
