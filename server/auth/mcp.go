package auth

import (
	"context"
	"net/http"

	mcpauth "github.com/modelcontextprotocol/go-sdk/auth"
)

// MCPTokenVerifier returns an MCP SDK auth.TokenVerifier that validates
// PocketCI JWT tokens. The returned TokenInfo includes scopes from the JWT
// claims and maps the user ID for session hijacking prevention.
//
// MCP validation is strict: the token must carry aud=mcp. A token minted
// for the API surface (aud=api) or a legacy unaudienced token is rejected
// here so that a stolen API token cannot read MCP data, and so an
// MCP-issued token cannot be replayed against /api/*. See PCI-SEC-MCP-001.
func MCPTokenVerifier(secret string) mcpauth.TokenVerifier {
	return func(_ context.Context, token string, _ *http.Request) (*mcpauth.TokenInfo, error) {
		claims, err := validateTokenClaims(token, secret, AudienceMCP, false)
		if err != nil {
			return nil, mcpauth.ErrInvalidToken
		}

		return &mcpauth.TokenInfo{
			Scopes:     claims.Scopes,
			Expiration: claims.ExpiresAt.Time,
			UserID:     claims.Subject,
		}, nil
	}
}
