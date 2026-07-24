-- Phase 1: run through a warm TiDB before starting the cold peer.
DROP DATABASE IF EXISTS ai_auto_reload_src;
DROP DATABASE IF EXISTS ai_auto_reload_dst;
CREATE DATABASE ai_auto_reload_src;
CREATE DATABASE ai_auto_reload_dst;

CREATE TABLE ai_auto_reload_src.pk_insert (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  v VARCHAR(32)
) AUTO_ID_CACHE=1;

CREATE TABLE ai_auto_reload_src.pk_replace (
  id BIGINT AUTO_INCREMENT PRIMARY KEY,
  v VARCHAR(32)
) AUTO_ID_CACHE=1;

CREATE TABLE ai_auto_reload_src.nonunique (
  id BIGINT NOT NULL AUTO_INCREMENT,
  v VARCHAR(32),
  KEY idx_id(id)
) AUTO_ID_CACHE=1;

INSERT INTO ai_auto_reload_src.pk_insert(v) VALUES ('old-1'), ('old-2');
INSERT INTO ai_auto_reload_src.pk_replace(v) VALUES ('order-1'), ('order-2');
INSERT INTO ai_auto_reload_src.nonunique(v) VALUES ('old-1'), ('old-2');

RENAME TABLE ai_auto_reload_src.pk_insert TO ai_auto_reload_dst.pk_insert;
RENAME TABLE ai_auto_reload_src.pk_replace TO ai_auto_reload_dst.pk_replace;
RENAME TABLE ai_auto_reload_src.nonunique TO ai_auto_reload_dst.nonunique;

SELECT TABLE_NAME, TIDB_TABLE_ID, TIDB_TABLE_MODE, AUTO_INCREMENT
FROM information_schema.tables
WHERE TABLE_SCHEMA = 'ai_auto_reload_dst'
ORDER BY TABLE_NAME;

-- Phase 2: start a new unmodified TiDB against the same PD/TiKV, then run
-- each cell separately through that cold TiDB.
SELECT VERSION();
SHOW GLOBAL VARIABLES LIKE 'tidb_enable_metadata_lock';

SELECT TABLE_NAME, AUTO_INCREMENT
FROM information_schema.tables
WHERE TABLE_SCHEMA = 'ai_auto_reload_dst'
ORDER BY TABLE_NAME;

-- Cell A: expected next ID is 3; current nightly returns duplicate key 2.
INSERT INTO ai_auto_reload_dst.pk_insert(v) VALUES ('new-insert');
SELECT id, v FROM ai_auto_reload_dst.pk_insert ORDER BY id;

-- Cell B: current nightly reports success and overwrites order-2 with generated ID 2.
REPLACE INTO ai_auto_reload_dst.pk_replace(v) VALUES ('replacement');
SELECT ROW_COUNT() AS affected_rows, LAST_INSERT_ID() AS generated_id;
SELECT id, v FROM ai_auto_reload_dst.pk_replace ORDER BY id;
ADMIN CHECK TABLE ai_auto_reload_dst.pk_replace;

-- Cell C: current nightly reports success and creates a duplicate generated identity.
INSERT INTO ai_auto_reload_dst.nonunique(v) VALUES ('new-cold-load');
SELECT ROW_COUNT() AS affected_rows, LAST_INSERT_ID() AS generated_id;
SELECT id, v FROM ai_auto_reload_dst.nonunique ORDER BY id, v;
SELECT id, COUNT(*) AS cnt
FROM ai_auto_reload_dst.nonunique
GROUP BY id
HAVING COUNT(*) > 1;
ADMIN CHECK TABLE ai_auto_reload_dst.nonunique;
