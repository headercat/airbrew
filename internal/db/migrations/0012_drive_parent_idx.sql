-- 0012_drive_parent_idx.sql
-- Adds a parent_id-leading index so the recursive-CTE subtree walks used by
-- move, trash, restore and delete resolve children by parent_id without a full
-- table scan as drive_nodes grows. The existing idx_drive_nodes_user_parent
-- leads on user_id, which the subtree recursion (which joins on parent_id)
-- cannot use.
CREATE INDEX IF NOT EXISTS idx_drive_nodes_parent ON drive_nodes(parent_id);
