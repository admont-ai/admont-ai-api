SET search_path TO admont_ai;

DROP TABLE IF EXISTS ai_messages;
DROP TABLE IF EXISTS ai_conversations;
DROP TABLE IF EXISTS user_group_members;
DROP TABLE IF EXISTS user_groups;
DROP TABLE IF EXISTS credentials;
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS search_repo_state;
DROP TABLE IF EXISTS git_repos;
DROP TABLE IF EXISTS search_providers;
DROP TABLE IF EXISTS llm_providers;
DROP TABLE IF EXISTS auth_providers;
DROP TABLE IF EXISTS settings;

DROP FUNCTION IF EXISTS update_updated_at();
DROP TYPE IF EXISTS user_status;
DROP TYPE IF EXISTS user_role;
