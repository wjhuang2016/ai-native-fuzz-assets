INSERT INTO found_bug
(id,title,severity,category,ddl_op,feature,symptom,repro,expected,actual,root_cause,fix_hint,oracle,method,root_cause_id,affects,confirmed,status,issue_url,notes)
VALUES
(3180003,
 'TiKV ignores default_week_format for pushed WEEK(date) and DELETE can remove the wrong dates',
 'high',
 'data-loss',
 'DELETE/UPDATE',
 'WEEK(date) coprocessor pushdown',
 'With a nonzero default_week_format, TiKV evaluates pushed single-argument WEEK(date) as mode 0. SELECT admits rows whose TiDB-projected predicate is false, and ordinary DELETE or UPDATE consumes those wrong handles without rechecking.',
 'Create a DATE table containing year-boundary values, set default_week_format=3, and compare WEEK(d) with WEEK(d,@@default_week_format). The pushed impossible predicate returns ids 1,2,3,6,7,9,10,11 while a root barrier returns none. On identical copies, DELETE WHERE WEEK(d)=52 removes ids 1,5,6,9 remotely but only id 5 locally.',
 'WEEK(date) must use the current session default_week_format exactly as WEEK(date,@@default_week_format) does. A pushed row-set consumer must select and mutate the same primary keys as TiDB.',
 'The semantically impossible predicate returned eight rows, each projected with equal implicit/explicit week values and predicate_holds=0. Pushed DELETE succeeded and removed four rows; root DELETE removed one. MDL remained ON, strict sql_mode remained at its default, and ADMIN CHECK passed after both operations.',
 'TiDB builtinWeekWithoutModeSig reads ctx.GetDefaultWeekFormatMode. TiKV week_without_mode ignores session context and calls WeekMode::from_bits_truncate(0). The remote signature/context has no default_week_format field.',
 'Rewrite single-argument WEEK to WeekWithMode with the current setting before pushdown, or serialize default_week_format into coprocessor EvalContext and consume it in TiKV. Disable WeekWithoutMode pushdown until the hidden input is represented.',
 'push-root-self-equivalence-delete-preimage',
 'remote-hidden-session-input-closure',
 'tikv-week-without-mode-default-week-format-context-omission',
 'TiDB nightly ed2376acc6; TiKV nightly 730be34f95; current TiDB 05b396fb66 and TiKV 91ccfb2126',
 1,
 'confirmed',
 NULL,
 'One TiDB, one PD, one real TiKV, MDL ON, default strict sql_mode, no prepared statement, plan cache dependency, concurrency, retry, failpoint, source patch, process pause, or node/network/disk fault. The only nondefault setting is default_week_format=3, commonly used for ISO-style weeks. Current master source still has the local getter versus remote hardcoded-mode split. Distinct from id30034/issue #69650 plan-cache staleness and older local/session loading issues. The consequence is direct silent data loss, but catalog severity remains high because nonzero default_week_format is required.');
