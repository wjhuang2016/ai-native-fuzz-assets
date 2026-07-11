# REVOKE table privilege can write wrong Grantor metadata through pooled sys session

Status: confirmed on testbed `8220955`; inserted into remote `found_bug` as id1260004.

## Summary

`REVOKE` on a table privilege can update `mysql.tables_priv.Grantor` from the pooled internal session's `SessionVars.User` instead of the user executing the `REVOKE` statement.

On testbed `8220955`, after user A grants `SELECT,INSERT` and user B revokes only `SELECT`, the privilege row remains with `Insert`, but `Grantor` becomes empty instead of identifying user B.

This is a SQL-visible privilege metadata correctness bug. The privilege set itself changed correctly in the observed repro; this is not a privilege-bypass or security-attack claim.

## Repro

```sql
DROP DATABASE IF EXISTS ai_grant_bug;
CREATE DATABASE ai_grant_bug;
CREATE TABLE ai_grant_bug.t(id INT PRIMARY KEY);

DROP USER IF EXISTS 'ai_grantor_a'@'%';
DROP USER IF EXISTS 'ai_grantor_b'@'%';
DROP USER IF EXISTS 'ai_target'@'%';
CREATE USER 'ai_grantor_a'@'%';
CREATE USER 'ai_grantor_b'@'%';
CREATE USER 'ai_target'@'%';

GRANT ALL PRIVILEGES ON ai_grant_bug.* TO 'ai_grantor_a'@'%' WITH GRANT OPTION;
GRANT ALL PRIVILEGES ON ai_grant_bug.* TO 'ai_grantor_b'@'%' WITH GRANT OPTION;
```

As `ai_grantor_a`:

```sql
GRANT SELECT, INSERT ON ai_grant_bug.t TO 'ai_target'@'%';
```

As `root`:

```sql
SELECT User, Host, DB, Table_name, Grantor, Table_priv, Column_priv
FROM mysql.tables_priv
WHERE User='ai_target' AND DB='ai_grant_bug' AND Table_name='t';
```

Observed:

```text
ai_target | % | ai_grant_bug | t | ai_grantor_a@% | Select,Insert | Select,Insert
```

As `ai_grantor_b`:

```sql
REVOKE SELECT ON ai_grant_bug.t FROM 'ai_target'@'%';
```

As `root`:

```sql
SELECT User, Host, DB, Table_name, Grantor, Table_priv, Column_priv
FROM mysql.tables_priv
WHERE User='ai_target' AND DB='ai_grant_bug' AND Table_name='t';
```

Observed on testbed `8220955`:

```text
ai_target | % | ai_grant_bug | t |  | Insert | Insert
```

## Expected

If `REVOKE` updates `mysql.tables_priv.Grantor`, the value should be derived from the current user executing the `REVOKE` statement (`ai_grantor_b@%` in this repro), not from an uninitialized or stale pooled internal session.

## Source Notes

- `pkg/executor/grant.go:151-152` gets a sys session and copies `e.Ctx().GetSessionVars().User` into `internalSession.GetSessionVars().User`.
- `pkg/executor/internal/exec/executor.go:581-591` releases a sys session by rolling back and returning it to the pool; it does not restore `SessionVars.User`.
- `pkg/executor/revoke.go:78-83` gets a sys session but does not initialize `SessionVars.User` from the outer user session.
- `pkg/executor/revoke.go:300` passes `internalSession` into `composeTablePrivUpdateForRevoke`.
- `pkg/executor/revoke.go:398` writes `Grantor` from `ctx.GetSessionVars().User.String()`.
- Existing integration coverage already treats `Grantor` as a visible field for `GRANT`: `tests/integrationtest/r/executor/grant.result:294-296` expects `root@%`.

## Evidence

- Testbed: `8220955`
- Version: `v9.0.0-beta.2.pre-1895-g5c9198e948`
- Evidence log: `/Users/bba/pc/ai-native-assets/logs/source-state-ingress-grant-revoke-sys-session-user-testbed8220955.log`
- Asset entry: `/Users/bba/pc/ai-native-assets/source-state-ingress-grant-revoke-results.jsonl`

## Method Lesson

The original source target was generated as `STATE_INGRESS_INTERNAL_SQL`, but the pending-`TxnReadTS` hypothesis retired after session-ownership proof. The bug appeared by continuing the same proof obligation one step deeper:

```text
internal pooled session has state P
system believes current statement metadata Q can be derived from it
metadata update writes user-visible Grantor
cross-user partial revoke makes P != current actor observable
```

This promotes a reusable selector: `SYS_SESSION_POOLED_STATE_ISOLATION`.
