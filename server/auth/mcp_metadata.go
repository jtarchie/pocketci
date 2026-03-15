package auth

import (
	"encoding/json"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
)

// MCPScope is the read-only scope issued to all MCP tokens.
const MCPScope = "ci:read"

// NewProtectedResourceMetadata builds RFC 9728 metadata for the MCP endpoint.
func NewProtectedResourceMetadata(baseURL string) *oauthex.ProtectedResourceMetadata {
	return &oauthex.ProtectedResourceMetadata{
		Resource:               baseURL,
		AuthorizationServers:   []string{baseURL},
		ScopesSupported:        []string{MCPScope},
		BearerMethodsSupported: []string{"header"},
		ResourceName:           "PocketCI",
	}
}

// ProtectedResourceMetadataHandler returns an http.Handler that serves
// RFC 9728 Protected Resource Metadata as JSON.
func ProtectedResourceMetadataHandler(meta *oauthex.ProtectedResourceMetadata) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusNoContent)

			return
		}

		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		_ = json.NewEncoder(w).Encode(meta)
	})
}

// authServerMetadata is a minimal RFC 8414 Authorization Server Metadata
// document. Defined here to avoid the build-tag-restricted oauthex.AuthServerMeta.
type authServerMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint,omitempty"`
	ScopesSupported                   []string `json:"scopes_supported,omitempty"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported,omitempty"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported,omitempty"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported,omitempty"`
}

// NewAuthServerMetadata builds RFC 8414 metadata for PocketCI acting as an
// OAuth authorization server for MCP clients.
func NewAuthServerMetadata(baseURL string) *authServerMetadata {
	return &authServerMetadata{
		Issuer:                            baseURL,
		AuthorizationEndpoint:             baseURL + "/oauth/authorize",
		TokenEndpoint:                     baseURL + "/oauth/token",
		RegistrationEndpoint:              baseURL + "/oauth/register",
		ScopesSupported:                   []string{MCPScope},
		ResponseTypesSupported:            []string{"code"},
		GrantTypesSupported:               []string{"authorization_code"},
		TokenEndpointAuthMethodsSupported: []string{"none"},
		CodeChallengeMethodsSupported:     []string{"S256"},
	}
}

// AuthServerMetadataHandler returns an http.Handler that serves RFC 8414
// Authorization Server Metadata as JSON.
func AuthServerMetadataHandler(meta *authServerMetadata) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.WriteHeader(http.StatusNoContent)

			return
		}

		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", "*")

		_ = json.NewEncoder(w).Encode(meta)
	})
}
