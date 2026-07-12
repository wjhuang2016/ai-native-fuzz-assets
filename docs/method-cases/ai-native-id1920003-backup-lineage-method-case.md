# id1920003: Follow a weak identity token to the final artifact

P: a completed backup range belongs to the source cluster and snapshot that produced it. Q: the
same config hash and storage prefix identify that source. F: the hash binds PD address strings, not
the cluster behind them, while checkpoint metadata has no cluster ID.

The selector did not stop at the missing field. It followed the accepted token through three
consumers: `CheckCheckpoint` admitted it, `GetTS` replaced the requested timestamp, and
`BuildProgressRangeTree` copied old SST files into the new backupmeta while deleting current work
from the incomplete set. That final consumer made the C3 oracle direct.

The small matrix was enough:

- RED: current cluster 222 plus old-lineage completed range -> accepted, BackupTS 100, zero current
  ranges, `old-cluster.sst` in backupmeta.
- GREEN: no checkpoint -> one current range remains for backup.
- Counterfactual GREEN: add source cluster ID 111 and compare it with 222 -> reject before artifact
  publication.

Method improvement: for every reusable token, mutate the semantic owner while preserving the weak
token, then observe both skipped current work and the final published artifact. A missing lineage
field is only a candidate; cross-lineage artifact publication is the proof.
