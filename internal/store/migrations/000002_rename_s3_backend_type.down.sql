-- Revert: rename 's3_store' back to 's3'.
UPDATE git_repos SET backend_type = 's3' WHERE backend_type = 's3_store';
