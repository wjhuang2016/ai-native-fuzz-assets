# id1020001 Method Case - account attribute validation before duplicate user classifier

Remote `found_bug`: id1020001, confirmed.

## Claim

S15 can apply outside `pkg/ddl` if the statement is still a DDL-like idempotence promise. The key
is not "try account options"; it is "pin the object identity, then vary only the discarded
candidate payload."

## Proof Obligation

```text
P_check:  anonymous users cannot use PASSWORD EXPIRE
Q_claim:  reject the statement before creating/modifying a user
fast path: ErrPasswordExpireAnonymousUser
safe path: same user already exists + IF NOT EXISTS -> Note 3163 / no-op
missing D: when the exact same user exists, candidate account attributes are not used
```

## Minimal Matrix

```text
target identity: ''@'ai_s15_host'

1. existing target + no candidate payload
   CREATE USER IF NOT EXISTS ''@'ai_s15_host'
   -> GREEN, Note 3163

2. existing target + unused invalid payload
   CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE
   -> RED, ERROR 3016

3. absent target + same invalid payload
   CREATE USER IF NOT EXISTS ''@'ai_s15_host' PASSWORD EXPIRE
   -> GREEN control, ERROR 3016 and no user row

4. existing row after RED
   mysql.user.Password_expired=N, Account_locked=N
   -> wrong-error, not partial write
```

## Source Pattern

`pkg/executor/simple.go`:

```text
1049 executeCreateUser
1099 plOptions.loadOptions(...)
1176 if len(spec.User.Username) == 0 && plOptions.passwordExpired == "Y"
1177     return ErrPasswordExpireAnonymousUser
1185 exists, err := userExists(...)
1189 if exists
1191   if !s.IfNotExists ...
1197   AppendNote(UserAlreadyExists)
```

The validator is above the duplicate classifier.

## Why This Worked

The selector started from a proof obligation, not from random account syntax:

```text
code checks P:  PASSWORD EXPIRE is invalid for anonymous users
system believes Q: reject before continuing
skipped safe path: userExists + IF NOT EXISTS duplicate no-op
missing dimension D: exact same user identity already exists
```

The decisive move was identity pinning. A long username, bad host, or missing resource group would
be too ambiguous because the "target" might not be the same object. The empty username plus unique
host lets the matrix hold identity constant and change only the candidate attribute.

## Negative Calibration

Do not scan all `CREATE USER` options. Reopen only for another identity-pinned account DDL path
where a candidate-only validator dominates `IF NOT EXISTS`/`IF EXISTS`.

`ALTER SEQUENCE IF EXISTS` and `ALTER RESOURCE GROUP IF EXISTS` are useful green controls: both
check target existence before validating options.

