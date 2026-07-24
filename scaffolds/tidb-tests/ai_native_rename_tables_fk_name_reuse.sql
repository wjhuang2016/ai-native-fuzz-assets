SELECT VERSION() AS tidb_version;
SELECT @@global.tidb_enable_foreign_key AS foreign_key_enabled;
SELECT @@global.tidb_enable_metadata_lock AS mdl_enabled;

SET GLOBAL tidb_enable_foreign_key = 1;
DROP DATABASE IF EXISTS ai_rename_fk;
CREATE DATABASE ai_rename_fk;
USE ai_rename_fk;

CREATE TABLE p1 (id INT PRIMARY KEY);
CREATE TABLE p2 (id INT PRIMARY KEY);
CREATE TABLE p3 (id INT PRIMARY KEY);
CREATE TABLE c1 (
  id INT PRIMARY KEY,
  pid INT,
  INDEX (pid),
  CONSTRAINT fk_c1 FOREIGN KEY (pid) REFERENCES p1(id)
);
INSERT INTO p1 VALUES (1);
INSERT INTO p3 VALUES (3);
INSERT INTO c1 VALUES (1, 1);

RENAME TABLE p1 TO tmp, p2 TO p1, tmp TO p2, p3 TO tmp;

SELECT referenced_table_name
FROM information_schema.referential_constraints
WHERE constraint_schema = 'ai_rename_fk'
  AND table_name = 'c1'
  AND constraint_name = 'fk_c1';

SELECT
  c.id,
  c.pid,
  EXISTS(SELECT 1 FROM p2 WHERE p2.id = c.pid) AS expected_parent_exists,
  EXISTS(SELECT 1 FROM tmp WHERE tmp.id = c.pid) AS bound_parent_exists
FROM c1 AS c
ORDER BY c.id;

INSERT INTO c1 VALUES (3, 3);
DELETE FROM p2 WHERE id = 1;

SELECT
  c.id,
  c.pid,
  EXISTS(SELECT 1 FROM p2 WHERE p2.id = c.pid) AS expected_parent_exists,
  EXISTS(SELECT 1 FROM tmp WHERE tmp.id = c.pid) AS bound_parent_exists
FROM c1 AS c
ORDER BY c.id;

ADMIN CHECK TABLE c1;

CREATE TABLE parent_old (id INT PRIMARY KEY);
CREATE TABLE child (
  id INT PRIMARY KEY,
  pid INT,
  INDEX (pid),
  CONSTRAINT fk_child FOREIGN KEY (pid) REFERENCES parent_old(id)
);
INSERT INTO parent_old VALUES (10);
RENAME TABLE parent_old TO parent_new;
INSERT INTO child VALUES (10, 10);

SELECT referenced_table_name
FROM information_schema.referential_constraints
WHERE constraint_schema = 'ai_rename_fk'
  AND table_name = 'child'
  AND constraint_name = 'fk_child';
