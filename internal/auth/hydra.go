package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HydraClient holds the credentials for an OAuth2 client registered in Hydra.
type HydraClient struct {
	ClientID     string
	ClientSecret string
}

// hydraClientRequest is the JSON body sent to Hydra's admin API to create a client.
type hydraClientRequest struct {
	ClientID                string   `json:"client_id,omitempty"`
	ClientSecret            string   `json:"client_secret,omitempty"`
	ClientName              string   `json:"client_name"`
	GrantTypes              []string `json:"grant_types"`
	ResponseTypes           []string `json:"response_types"`
	Scope                   string   `json:"scope"`
	RedirectURIs            []string `json:"redirect_uris"`
	TokenEndpointAuthMethod string   `json:"token_endpoint_auth_method"`
}

// hydraClientResponse is the JSON body returned by Hydra's admin API.
type hydraClientResponse struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

// EnsureHydraClient ensures an OAuth2 client exists in Hydra for the given redirect URIs.
// If clientID is provided, it checks whether the client exists and returns its credentials.
// If the client does not exist, it creates one. Returns the client credentials.
func EnsureHydraClient(ctx context.Context, adminURL, clientID, clientSecret string, redirectURIs []string) (*HydraClient, error) {
	httpClient := &http.Client{Timeout: 10 * time.Second}

	// If we have a client ID, check if it already exists in Hydra.
	if clientID != "" {
		existing, err := getHydraClient(ctx, httpClient, adminURL, clientID)
		if err == nil && existing != nil {
			// Client exists — update redirect URIs to stay in sync.
			if err := updateHydraClient(ctx, httpClient, adminURL, clientID, redirectURIs); err != nil {
				return nil, fmt.Errorf("updating hydra client: %w", err)
			}
			return &HydraClient{ClientID: clientID, ClientSecret: clientSecret}, nil
		}
	}

	return createHydraClient(ctx, httpClient, adminURL, clientID, clientSecret, redirectURIs)
}

// createHydraClient posts a new client to Hydra's admin API.
func createHydraClient(ctx context.Context, httpClient *http.Client, adminURL, clientID, clientSecret string, redirectURIs []string) (*HydraClient, error) {
	body := hydraClientRequest{
		ClientID:                clientID,
		ClientSecret:            clientSecret,
		ClientName:              "admont-ai",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		Scope:                   "openid email profile offline_access",
		RedirectURIs:            redirectURIs,
		TokenEndpointAuthMethod: "client_secret_post",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling hydra client request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, adminURL+"/admin/clients", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("creating hydra client request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("creating hydra client: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, &hydraError{StatusCode: resp.StatusCode, Body: string(respBody)}
	}

	var created hydraClientResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return nil, fmt.Errorf("decoding hydra client response: %w", err)
	}

	return &HydraClient{
		ClientID:     created.ClientID,
		ClientSecret: created.ClientSecret,
	}, nil
}

// hydraError captures a non-2xx response from Hydra.
type hydraError struct {
	StatusCode int
	Body       string
}

func (e *hydraError) Error() string {
	return fmt.Sprintf("hydra client creation failed (status %d): %s", e.StatusCode, e.Body)
}

// isHydraSchemaError returns true if the error looks like Hydra's tables are missing.
func isHydraSchemaError(err error) bool {
	he, ok := err.(*hydraError)
	if !ok {
		return false
	}
	if he.StatusCode == http.StatusInternalServerError {
		return true
	}
	// Also catch connection refused (Hydra not up yet).
	return strings.Contains(he.Body, "Unable to locate the table")
}

// getHydraClient fetches an existing client from Hydra's admin API.
func getHydraClient(ctx context.Context, client *http.Client, adminURL, clientID string) (*hydraClientResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, adminURL+"/admin/clients/"+clientID, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	var result hydraClientResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// updateHydraClient replaces the client via Hydra's admin PUT endpoint with the full payload.
func updateHydraClient(ctx context.Context, client *http.Client, adminURL, clientID string, redirectURIs []string) error {
	body := hydraClientRequest{
		ClientName:              "admont-ai",
		GrantTypes:              []string{"authorization_code", "refresh_token"},
		ResponseTypes:           []string{"code"},
		Scope:                   "openid email profile offline_access",
		RedirectURIs:            redirectURIs,
		TokenEndpointAuthMethod: "client_secret_post",
	}

	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, adminURL+"/admin/clients/"+clientID, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("unexpected status %d: %s", resp.StatusCode, string(respBody))
	}
	return nil
}
