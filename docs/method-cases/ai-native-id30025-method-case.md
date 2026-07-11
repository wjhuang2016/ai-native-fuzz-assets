# Method Case id30025: Green calibration can point to the next red boundary
> 2026-07-03. Prepared plan cache + named timezone historical rules + `UNIX_TIMESTAMP` literals.

## Why This Target Was Picked

id30024 showed that session/config variables consumed while building a cached object must be covered by the cache key or force rebuild.

The next source fact was sharper:

```text
NewPlanCacheKey includes time_zone only as the current offset.
```

The first timezone matrix had been green for TIMESTAMP range rebuild. That did not kill the selector; it told us the hit path was rebuilding that boundary under the current session context. So the next question was:

```text
is there a timezone-dependent semantic boundary that is folded into the cached object
instead of rebuilt after cache hit?
```

`UNIX_TIMESTAMP('datetime literal')` fit:

- it depends on the session timezone;
- named zones can share the current offset but differ historically;
- with a literal argument, it can be folded during expression construction;
- flush reference is the same prepared statement after clearing the plan cache.

## Tiny Matrix

Red direction 1:

```text
Africa/Johannesburg fill -> Europe/Amsterdam hit
literal date: 2025-01-15 12:00:00
direct Johannesburg: 1736935200
direct Amsterdam:    1736938800
cached after toggle: 1736935200, @@last_plan_from_cache=1
flush reference:     1736938800, @@last_plan_from_cache=0
```

Red direction 2:

```text
Europe/Amsterdam fill -> Africa/Johannesburg hit
cached after toggle: 1736938800
flush reference:     1736935200
```

Green control:

```text
2025-07-15 12:00:00
both zones have the same historical offset, so cached/direct/flush all return 1752573600
```

Boundary:

```text
UNIX_TIMESTAMP(?) did not hit prepared plan cache in the probe, so the minimal confirmed surface is literal/constant-folded arguments.
```

## Why It Worked

The efficient move was not to enumerate time functions. It was to compare two semantic-boundary placements:

```text
TIMESTAMP range:
  cache hit -> rebuild range under current timezone -> GREEN

UNIX_TIMESTAMP literal:
  cache hit -> reuse folded constant from old timezone -> RED
```

So the selector became more precise:

```text
coarse key dimension + cached folded/evaluated value + same-query flush reference
```

## Quality

High:

- user-visible wrong result;
- no failpoint and no timing race;
- normal prepared statement and session `time_zone`;
- bidirectional RED;
- flush reference uses the same prepared statement;
- green date control prevents overclaiming that all timezone cache hits are wrong.

## Methodology Improvement

Add a fourth S7 sub-check:

```text
coarse-key sufficiency:
  if the key stores an approximation of a semantic dimension,
  prove every cached boundary is either rebuilt under current context
  or is independent of the missing detail.
```

The key lesson: a green calibration is often a map. It tells us which boundary was safe, and therefore where to look for a boundary that is not rebuilt.
