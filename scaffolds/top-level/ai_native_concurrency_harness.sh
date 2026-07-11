#!/bin/zsh
# ai_native_concurrency_harness.sh — reusable harness for the interleaving dimension.
# Runs an online DDL under a concurrent DML load, detects the three failure modes
# (loud error / invalid-encoded-key / WEDGE), and always runs silent-consequence oracles.
# Generalizes the id30038 hunt. Spec: ai-native-concurrency-harness.md
#
# Usage: zsh ai_native_concurrency_harness.sh <case_file> [iterations] [cap_seconds]
# The case file is sourced and must define:
#   CASE_NAME, DB, SETUP_SQL, DDL_SQL, dml_for_iter() ; optional SILENT_SQL
# Design: the DML feeder runs as a SEPARATE background process so a DML that blocks on a wedged
# DDL cannot stall the poll loop. The main loop polls ADMIN SHOW DDL (independent connection) on a
# timer with a hard per-iteration cap, so the harness never hangs.

set -u
MHOST=${MHOST:-127.0.0.1}
MPORT=${MPORT:-4000}
MUSER=${MUSER:-root}
DDL_DIST_TASK=${DDL_DIST_TASK:-OFF}
DDL_FAST_REORG=${DDL_FAST_REORG:-OFF}
DDL_REORG_WORKER_CNT=${DDL_REORG_WORKER_CNT:-1}
DDL_REORG_BATCH_SIZE=${DDL_REORG_BATCH_SIZE:-32}
DML_FEEDERS=${DML_FEEDERS:-1}
q()  { mysql --host $MHOST --port $MPORT -u $MUSER "$@"; }
qdb(){ mysql --host $MHOST --port $MPORT -u $MUSER -D "$DB" "$@"; }

CASE=${1:?need a case file}; source "$CASE"
ITERS=${2:-6}; CAP=${3:-90}
: ${CASE_NAME:?}; : ${DB:?}; : ${SETUP_SQL:?}; : ${DDL_SQL:?}

q -e "SET GLOBAL tidb_enable_dist_task=${DDL_DIST_TASK};
      SET GLOBAL tidb_ddl_enable_fast_reorg=${DDL_FAST_REORG};
      SET GLOBAL tidb_ddl_reorg_worker_cnt=${DDL_REORG_WORKER_CNT};
      SET GLOBAL tidb_ddl_reorg_batch_size=${DDL_REORG_BATCH_SIZE};" 2>/dev/null

errcount() { q -e "ADMIN SHOW DDL\G" 2>/dev/null | grep -oE "ErrCount:[0-9]+" | head -1 | grep -oE "[0-9]+"; }
schemastate(){ q -e "ADMIN SHOW DDL\G" 2>/dev/null | grep -oE "SchemaState:[a-z ]+" | head -1; }

for iter in $(seq 1 $ITERS); do
  q -e "DROP DATABASE IF EXISTS $DB; CREATE DATABASE $DB;" >/dev/null 2>&1
  qdb -e "$SETUP_SQL" >/dev/null 2>&1

  # background DML feeders (decoupled from polling)
  typeset -a FEEDERS
  for wid in $(seq 1 $DML_FEEDERS); do
    ( while :; do dml_for_iter $iter $wid | qdb 2>/dev/null; done ) &
    FEEDERS+=($!)
  done

  qdb -e "$DDL_SQL" > /tmp/ainh_ddl_$iter.txt 2>&1 &
  APID=$!

  WEDGE=""; e1=""; elapsed=0
  while kill -0 $APID 2>/dev/null; do
    sleep 3; elapsed=$((elapsed+3))
    e2=$(errcount); ss=$(schemastate)
    if [ -n "$e1" ] && [ -n "$e2" ] && [ "$e2" -gt "$e1" ] 2>/dev/null; then
      WEDGE="ErrCount $e1->$e2 climbing, $ss"; break
    fi
    [ -n "$e2" ] && e1=$e2
    if [ $elapsed -ge $CAP ]; then WEDGE="no client return after ${CAP}s, ${ss:-no-state}"; break; fi
  done

  for FEEDER in $FEEDERS; do
    kill $FEEDER 2>/dev/null
    pkill -P $FEEDER 2>/dev/null
  done
  [ -n "$WEDGE" ] && kill $APID 2>/dev/null                # release the DDL client if wedged
  wait $APID 2>/dev/null
  RES=$(cat /tmp/ainh_ddl_$iter.txt 2>/dev/null)

  # let a wedged job settle (it rolls back once traffic stops); give it a bounded grace
  if [ -n "$WEDGE" ]; then g=0; while [ $g -lt 30 ]; do
    q -e "ADMIN SHOW DDL\G" 2>/dev/null | grep -qi "RUNNING_JOBS: $" && break
    q -e "ADMIN SHOW DDL\G" 2>/dev/null | grep -q "ID:" || break; sleep 3; g=$((g+3)); done
  fi

  # --- silent-consequence oracles (run regardless of loud outcome) ---
  CHK=$(qdb -e "ADMIN CHECK TABLE t;" 2>&1 | grep -iE "error|inconsistent|8003|8223" | head -1)
  SIL=""; [ -n "${SILENT_SQL:-}" ] && SIL=$(qdb -Ne "$SILENT_SQL" 2>/dev/null | head -1)

  if   [ -n "$CHK" ]; then echo "iter$iter [$CASE_NAME] RED_CORRUPT (silent): ADMIN CHECK failed: $CHK"
  elif [ -n "$SIL" ]; then echo "iter$iter [$CASE_NAME] RED_CORRUPT (silent): invariant probe: $SIL"
  elif [ -n "$WEDGE" ]; then echo "iter$iter [$CASE_NAME] RED_WEDGE (O28 liveness C3): $WEDGE"
  elif echo "$RES" | grep -qi "invalid encoded key"; then echo "iter$iter [$CASE_NAME] RED_ENCKEY: $(echo $RES | tr -d '\n' | cut -c1-70)"
  elif echo "$RES" | grep -qiE "ERROR [0-9]"; then echo "iter$iter [$CASE_NAME] RED_ERROR (loud): $(echo $RES | tr -d '\n' | cut -c1-70)"
  else echo "iter$iter [$CASE_NAME] GREEN: DDL ok, ADMIN CHECK ok, invariant clean"; fi
done
echo "=== $CASE_NAME done ($ITERS iters, cap ${CAP}s) ==="
