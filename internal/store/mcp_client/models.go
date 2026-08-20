package mcp_client

import "time"

// RegisteredClient is an MCP OAuth client registered via RFC 7591 dynamic
// client registration (POST /mcp/register).
type RegisteredClient struct {
	ClientID     string
	RedirectURIs []string
	CreatedAt    time.Time
}
