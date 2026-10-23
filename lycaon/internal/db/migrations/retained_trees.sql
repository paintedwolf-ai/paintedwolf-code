-- Released plans have no retained-tree binding. Keep their blob manifests
-- unchanged and refuse an unrecognized binding rather than interpreting it.
CREATE TEMP TABLE retained_tree_upgrade_guard (invalid INTEGER NOT NULL CHECK (invalid = 0));
INSERT INTO retained_tree_upgrade_guard
SELECT count(*) FROM source_mutations WHERE CASE
 WHEN NULLIF(plan_json,'') IS NULL THEN 0
 WHEN NOT json_valid(plan_json) THEN 1
 WHEN json_type(plan_json,'$.retained_tree') IS NOT NULL OR json_type(plan_json,'$.entry_recovery_key') IS NOT NULL THEN 1 ELSE 0 END;
INSERT INTO retained_tree_upgrade_guard
SELECT count(*) FROM source_history_entries WHERE
 NOT json_valid(undo_plan_json) OR NOT json_valid(redo_plan_json)
 OR json_type(undo_plan_json,'$.retained_tree') IS NOT NULL
 OR json_type(redo_plan_json,'$.retained_tree') IS NOT NULL
 OR json_type(undo_plan_json,'$.entry_recovery_key') IS NOT NULL
 OR json_type(redo_plan_json,'$.entry_recovery_key') IS NOT NULL;
UPDATE source_mutations SET plan_json=json_set(plan_json,'$.retained_tree',NULL) WHERE NULLIF(plan_json,'') IS NOT NULL;
UPDATE source_history_entries SET undo_plan_json=json_set(undo_plan_json,'$.retained_tree',NULL),redo_plan_json=json_set(redo_plan_json,'$.retained_tree',NULL);
DROP TABLE retained_tree_upgrade_guard;
