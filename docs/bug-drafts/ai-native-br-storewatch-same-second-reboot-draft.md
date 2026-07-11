# BR storewatch can miss reboot notification when StartTimestamp collides within one second

## Status

- Current master candidate: `13282a8bd06b`
- Evidence level: unit-level RED plus local-fix GREEN
- Remote `found_bug`: id1260007, `status=current-red-green,confirmed=0`
- Asset result: `/Users/bba/pc/ai-native-assets/source-storewatch-reboot-same-second-results.jsonl`
- Selector: `selector.identity-token-async-filter.v1`

## User-Visible Shape

BR backup and restore use `storewatch` callbacks to react to TiKV store lifecycle changes.

- Backup: `OnReboot` and `OnDisconnect` set the retry policy to send all stores again.
- Restore: `OnReboot` records rebooted stores so recovery can regenerate leaders.

Current `storewatch` only calls `OnReboot` when `Store.StartTimestamp` changes. If a store is observed as:

```text
Up(StartTimestamp=T)
Offline(StartTimestamp=T)
Up(StartTimestamp=T)
```

then current code fires `OnDisconnect` for the Offline state but does not fire `OnReboot` when the store returns Up. If the StartTimestamp token has only second-level precision, a fast restart/recovery can be missed by BR recovery users.

## Source Proof Obligation

```text
P: updateStore sees the same store ID and compares old/new StartTimestamp.
Q: unchanged StartTimestamp proves no reboot/recovery notification is needed.
F: StartTimestamp is a coarse lifecycle token; Offline->Up is itself a recovery edge and can occur with the same token value.
```

Relevant source:

- `/Users/bba/pc/tidb/br/pkg/utils/storewatch/watching.go`
- `/Users/bba/pc/tidb/br/pkg/backup/store.go`
- `/Users/bba/pc/tidb/br/pkg/restore/data/data.go`

## RED

Temporary test:

```text
br/pkg/utils/storewatch/watching_test.go::TestAINativeOnRebootWhenStoreRestartsWithinSameSecondRED
```

Command:

```text
go test ./br/pkg/utils/storewatch -run '^TestAINativeOnRebootWhenStoreRestartsWithinSameSecondRED$' -count=1 -timeout 60s -v
```

Observed:

```text
Error: Should be true
Messages: Up->Offline->Up with the same second-level StartTimestamp must still notify reboot/recovery users
```

RED log:

```text
/Users/bba/pc/ai-native-assets/logs/source-storewatch-current-reboot-same-second-red.log
```

## Local GREEN

Fix shape:

```text
OnReboot if:
  StartTimestamp changed
  OR previous state was not Up and current state is Up
```

This keeps the old StartTimestamp-change behavior and adds a conservative recovery edge for BR users.

Command:

```text
go test ./br/pkg/utils/storewatch -count=1 -timeout 60s -v
```

Observed:

```text
TestOnRegister PASS
TestOnOffline PASS
TestOnReboot PASS
TestAINativeOnRebootWhenStoreRestartsWithinSameSecond PASS
```

GREEN log:

```text
/Users/bba/pc/ai-native-assets/logs/source-storewatch-local-green.log
```

## Scope Discipline

This proves a watcher/callback contract bug candidate, not live TiKV restart frequency. The live lift would restart a TiKV store quickly enough to preserve the same start token and observe whether backup/restore receives the all-store retry or recovery signal.

The important method point is that this target passed the G3 schedule gate that BR registry heartbeat failed: `Offline->Up` is a product-visible lifecycle edge, not only a synthetic same-second timestamp.
