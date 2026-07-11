# Start Here

Use this order when resuming the system or onboarding a new agent.

## 1. Reconstruct the latest state

Read:

- `../handoff/ai-native-fuzz-handoff.md`
- `ai-native-autonomous-loop.md`

This tells you what was last confirmed, which hypotheses were retired, and which selectors are currently load-bearing.

## 2. Rebuild the method, not just the facts

Read:

- `ai-native-proof-obligation-methodology-v2.md`
- `ai-native-ddl-methodology.md`
- `ai-native-ddl-github-heldout-methodology.md`

Focus on the recurring loop:

1. Find a proof obligation.
2. Compress it into a small matrix.
3. Use a strong oracle to validate the red cell.
4. Pause after the hit and back-solve the selector/root cause.

## 3. Reuse existing assets before inventing new ones

Read:

- `ai-native-selector-ledger.md`
- `ai-native-root-cause-ledger.md`
- `ai-native-oracle-library.md`
- `ai-native-proof-obligation-catalog.md`

These are the reuse layer. They help avoid restarting from zero every round.

## 4. Pull the right execution scaffold

- Live probes: `../../scaffolds/go-probes/`
- Root-level Python/shell harnesses: `../../scaffolds/top-level/`
- TiDB-side fixtures: `../../scaffolds/tidb-tests/`
- Evidence and seed store: `../../assets/store/`

## 5. When exploring a new lane

Prefer this sequence:

1. Search the ledgers for a near-miss or sibling first.
2. Reuse an existing oracle if one already captures the consequence you care about.
3. Only then add a new probe or cluster-side harness.
4. After a hit, write both a bug draft and a compact method case.
