package auth_test

import (
	"strings"
	"testing"
	"time"

	"github.com/jtarchie/pocketci/server/auth"
)

// TestAudience_MCPTokenRejectedByAPI codifies PCI-SEC-MCP-001: a JWT minted
// for the MCP audience (aud=mcp) must NOT authenticate against the API
// surface. Before this fix, RequireAuth's validator ignored the Scopes
// claim and would accept any well-signed JWT, turning a read-only MCP
// token into a full /api/* writer.
func TestAudience_MCPTokenRejectedByAPI(t *testing.T) {
	t.Parallel()

	const secret = "supersecretsupersecretsupersecre"

	user := &auth.User{UserID: "u1", Email: "u@example.com", Provider: "github"}

	mcpToken, err := auth.GenerateToken(user, secret, time.Hour, []string{"ci:read"}, auth.AudienceMCP)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	apiValidator := auth.TokenValidator(secret)

	_, err = apiValidator(mcpToken)
	if err == nil {
		t.Fatal("API validator accepted MCP-audience token; chained takeover (PCI-SEC-MCP-001) still possible")
	}
	if !strings.Contains(err.Error(), "audience") {
		t.Fatalf("expected audience-mismatch error, got: %v", err)
	}
}

// TestAudience_APITokenRejectedByMCP — and the symmetric case: an API
// token must NOT pass MCP verification. Otherwise a stolen CLI token
// gains MCP-read access it was never granted.
func TestAudience_APITokenRejectedByMCP(t *testing.T) {
	t.Parallel()

	const secret = "supersecretsupersecretsupersecre"

	user := &auth.User{UserID: "u1", Email: "u@example.com", Provider: "github"}

	apiToken, err := auth.GenerateToken(user, secret, time.Hour, nil, auth.AudienceAPI)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	mcpVerifier := auth.MCPTokenVerifier(secret)

	_, err = mcpVerifier(t.Context(), apiToken, nil)
	if err == nil {
		t.Fatal("MCP verifier accepted API-audience token")
	}
}

// TestAudience_LegacyTokenAcceptedByAPI confirms the deliberate backward-
// compatibility window: a JWT minted before audience binding shipped (no
// "aud" claim at all) is still accepted by /api/* so existing CLI users
// don't lose access on upgrade. MCP rejects such tokens.
func TestAudience_LegacyTokenAcceptedByAPI(t *testing.T) {
	t.Parallel()

	const secret = "supersecretsupersecretsupersecre"

	user := &auth.User{UserID: "u1", Email: "u@example.com", Provider: "github"}

	legacyToken, err := auth.GenerateToken(user, secret, time.Hour, nil, "")
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	// API still accepts.
	apiValidator := auth.TokenValidator(secret)
	if _, err := apiValidator(legacyToken); err != nil {
		t.Fatalf("API validator rejected legacy (no-aud) token: %v", err)
	}

	// MCP must NOT — strict audience required.
	mcpVerifier := auth.MCPTokenVerifier(secret)
	if _, err := mcpVerifier(t.Context(), legacyToken, nil); err == nil {
		t.Fatal("MCP verifier accepted legacy (no-aud) token; should require aud=mcp")
	}
}

// TestAudience_APITokenAcceptedByAPI / MCPTokenAcceptedByMCP — happy paths.
func TestAudience_HappyPaths(t *testing.T) {
	t.Parallel()

	const secret = "supersecretsupersecretsupersecre"

	user := &auth.User{UserID: "u1", Email: "u@example.com", Provider: "github"}

	apiToken, err := auth.GenerateToken(user, secret, time.Hour, nil, auth.AudienceAPI)
	if err != nil {
		t.Fatalf("GenerateToken api: %v", err)
	}
	mcpToken, err := auth.GenerateToken(user, secret, time.Hour, []string{"ci:read"}, auth.AudienceMCP)
	if err != nil {
		t.Fatalf("GenerateToken mcp: %v", err)
	}

	if _, err := auth.TokenValidator(secret)(apiToken); err != nil {
		t.Fatalf("api token rejected by api validator: %v", err)
	}
	info, err := auth.MCPTokenVerifier(secret)(t.Context(), mcpToken, nil)
	if err != nil {
		t.Fatalf("mcp token rejected by mcp verifier: %v", err)
	}
	if len(info.Scopes) != 1 || info.Scopes[0] != "ci:read" {
		t.Fatalf("expected ci:read scope, got %v", info.Scopes)
	}
}

// TestAudience_WrongSignatureRejected: signature failures are still the
// dominant rejection reason regardless of audience handling.
func TestAudience_WrongSignatureRejected(t *testing.T) {
	t.Parallel()

	user := &auth.User{UserID: "u1", Email: "u@example.com", Provider: "github"}

	token, err := auth.GenerateToken(user, "secretA-32-bytes--padding----xxx", time.Hour, nil, auth.AudienceAPI)
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	if _, err := auth.TokenValidator("secretB-32-bytes--padding----xxx")(token); err == nil {
		t.Fatal("token validated against wrong secret")
	}
}
