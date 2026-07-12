# Method case: failed publication must retain retry ownership

Remote `found_bug`: `id1740003`, confirmed high.

## How the LOOP found it

The candidate came from current-source safety claims, not PR review. The scan looked for a public
or durable batch whose producer reports an error but then resets, closes, acknowledges, or advances
the only state needed to retry. In `batchFlusher.flush`, `err != nil` updates metrics while the
buffer reset remains unconditional.

## Selector S37

`FAILED_PUBLICATION_RETAINS_RETRY_OWNERSHIP`

For every asynchronous publication path, identify the record owner before the attempt and the
owner after failure. An error is not safely handled merely because it is logged or counted. At
least one owner must retain the exact failed payload until retry, compensation, or explicit
operator-visible rejection.

## Minimal matrix

| First publication | Recovery | Required result |
| --- | --- | --- |
| transient error | next flush healthy | original batch is published |
| success | no fault | batch is published once |
| transient error | retain-on-error counterfactual | next flush publishes original batch |

The local current-source row was RED (`attempts=1`, no persisted rule). Moving only the reset into
the success branch made all controls GREEN. The live lift then used a local-enforcement observer
and a fresh-node observer to prove that the missing durable row changes policy behavior.

## Improvement to the LOOP

Add an error-path owner-conservation pass after P/Q/F extraction:

1. Name the payload owner before the fallible action.
2. Force the action to fail once and then recover.
3. Verify that the next attempt receives the same payload.
4. Observe a fresh consumer, not only the producer's local cache.
5. Reject the candidate if another downstream owner reconstructs or rejects the missing state.

This complements the layered-dominance gate: the former asks whether a weak guard is caught later;
S37 asks whether failure destroys the only material needed for later recovery.
