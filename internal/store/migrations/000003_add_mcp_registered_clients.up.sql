SET search_path TO admont_ai;

CREATE TABLE mcp_registered_clients (
    client_id     TEXT PRIMARY KEY,
    redirect_uris TEXT[] NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
