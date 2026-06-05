-- Rename backend_type 's3' to 's3_store' for existing repos.
UPDATE git_repos SET backend_type = 's3_store' WHERE backend_type = 's3';
