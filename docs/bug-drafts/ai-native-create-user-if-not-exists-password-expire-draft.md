# CREATE USER IF NOT EXISTS fails before duplicate user no-op

Remote `found_bug`: id1020001, confirmed.

## Summary

`CREATE USER IF NOT EXISTS` can fail even when the exact same user already exists. The failing
candidate attribute is `PASSWORD EXPIRE` on an anonymous user:

```sql
CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE;
```

The already-existing user should make the statement an idempotent no-op, but TiDB validates
`PASSWORD EXPIRE` for anonymous users before checking whether the user exists.

## User-Visible Symptom

An idempotent account-bootstrap script can fail on rerun:

```sql
CREATE USER IF NOT EXISTS ''@'ai_s15_host';
CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE;
```

The second statement returns:

```text
ERROR 3016 (HY000): The password for anonymous user cannot be expired.
```

The existing row is not modified.

## Repro

Confirmed on testbed `8192975`.

```sql
DROP USER IF EXISTS ''@'ai_s15_host';
CREATE USER ''@'ai_s15_host';

SELECT User, Host, Password_expired, Account_locked
FROM mysql.user
WHERE User='' AND Host='ai_s15_host';

CREATE USER IF NOT EXISTS ''@'ai_s15_host';
SHOW WARNINGS;

CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE;
SHOW WARNINGS;

SELECT User, Host, Password_expired, Account_locked
FROM mysql.user
WHERE User='' AND Host='ai_s15_host';

DROP USER IF EXISTS ''@'ai_s15_host';
CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE;
SHOW WARNINGS;

SELECT COUNT(*)
FROM mysql.user
WHERE User='' AND Host='ai_s15_host';
```

Observed:

```text
CREATE USER IF NOT EXISTS ''@'ai_s15_host'
  -> Note 3163 User ''@'ai_s15_host' already exists.

CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE
  -> ERROR 3016 The password for anonymous user cannot be expired.

Existing row after the error:
  Password_expired = N
  Account_locked = N

Target-absent control:
  CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE
  -> ERROR 3016 and COUNT(*) = 0
```

## Expected

If the exact same user identity already exists and `IF NOT EXISTS` is present, TiDB should classify
the statement as a no-op with Note 3163 before validating account attributes that would be
discarded.

`PASSWORD EXPIRE` for an anonymous user should still hard-error when the target user is absent.

## Actual

TiDB returns `ERROR 3016` before reaching the duplicate-user no-op path.

## Source Anchor

`pkg/executor/simple.go`:

```text
executeCreateUser
  load password options
  for each spec:
    if len(username)==0 && passwordExpired=="Y" -> ErrPasswordExpireAnonymousUser
    userExists(...)
    if exists && IfNotExists -> append Note 3163 and continue
```

The validator at lines 1176-1177 dominates the duplicate classifier at lines 1185-1198.

## Quality

Severity: low.

Category: wrong-error.

No data corruption was observed. The existing user remains unchanged, and the absent-target control
still errors and creates no user. The quality is mostly methodological: S15 generalizes from normal
DDL objects into account DDL, but only after the identity is pinned to the same username and host.

## Method Lesson

This is not a reason to enumerate all account options. The useful selector is narrower:

```text
same account identity exists
IF NOT EXISTS is present
candidate account attribute is discarded on duplicate
attribute validator runs before userExists
```

Green controls:

- `ALTER SEQUENCE IF EXISTS` checks target existence before validating new options.
- `ALTER RESOURCE GROUP IF EXISTS` checks target existence before building/validating options.

