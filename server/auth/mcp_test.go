package auth_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/sessions"
	"github.com/jtarchie/pocketci/server/auth"
	. "github.com/onsi/gomega"
)

func TestMCPTokenVerifier(t *testing.T) {
	t.Parallel()
	secret := "test-secret-key-at-least-32-bytes-long"

	t.Run("valid token with scopes", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		user := &auth.User{
			Email:    "alice@example.com",
			Name:     "Alice",
			NickName: "alice",
			Provider: "github",
			UserID:   "12345",
		}

		token, err := auth.GenerateToken(user, secret, 24*time.Hour, []string{"ci:read"})
		assert.Expect(err).NotTo(HaveOccurred())

		verifier := auth.MCPTokenVerifier(secret)
		info, err := verifier(t.Context(), token, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(info.Scopes).To(ConsistOf("ci:read"))
		assert.Expect(info.UserID).To(Equal("12345"))
		assert.Expect(info.Expiration).NotTo(BeZero())
	})

	t.Run("valid token without scopes", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		user := &auth.User{
			Email:    "bob@example.com",
			UserID:   "67890",
			Provider: "github",
		}

		token, err := auth.GenerateToken(user, secret, 24*time.Hour, nil)
		assert.Expect(err).NotTo(HaveOccurred())

		verifier := auth.MCPTokenVerifier(secret)
		info, err := verifier(t.Context(), token, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(info.Scopes).To(BeNil())
		assert.Expect(info.UserID).To(Equal("67890"))
	})

	t.Run("expired token", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		user := &auth.User{UserID: "12345", Provider: "github"}

		token, err := auth.GenerateToken(user, secret, -1*time.Hour, []string{"ci:read"})
		assert.Expect(err).NotTo(HaveOccurred())

		verifier := auth.MCPTokenVerifier(secret)
		_, err = verifier(t.Context(), token, nil)
		assert.Expect(err).To(HaveOccurred())
	})

	t.Run("invalid token", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		verifier := auth.MCPTokenVerifier(secret)
		_, err := verifier(t.Context(), "not-a-token", nil)
		assert.Expect(err).To(HaveOccurred())
	})
}

func TestProtectedResourceMetadata(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	meta := auth.NewProtectedResourceMetadata("https://ci.example.com")
	assert.Expect(meta.Resource).To(Equal("https://ci.example.com"))
	assert.Expect(meta.AuthorizationServers).To(ConsistOf("https://ci.example.com"))
	assert.Expect(meta.ScopesSupported).To(ConsistOf("ci:read"))
	assert.Expect(meta.BearerMethodsSupported).To(ConsistOf("header"))
	assert.Expect(meta.ResourceName).To(Equal("PocketCI"))
}

func TestProtectedResourceMetadataHandler(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	meta := auth.NewProtectedResourceMetadata("https://ci.example.com")
	handler := auth.ProtectedResourceMetadataHandler(meta)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-protected-resource", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Expect(rec.Code).To(Equal(http.StatusOK))
	assert.Expect(rec.Header().Get("Content-Type")).To(Equal("application/json"))
	assert.Expect(rec.Header().Get("Access-Control-Allow-Origin")).To(Equal("*"))

	var body map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(body["resource"]).To(Equal("https://ci.example.com"))
	assert.Expect(body["resource_name"]).To(Equal("PocketCI"))
}

func TestAuthServerMetadata(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	meta := auth.NewAuthServerMetadata("https://ci.example.com")
	handler := auth.AuthServerMetadataHandler(meta)

	req := httptest.NewRequest(http.MethodGet, "/.well-known/oauth-authorization-server", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	assert.Expect(rec.Code).To(Equal(http.StatusOK))

	var body map[string]any
	err := json.Unmarshal(rec.Body.Bytes(), &body)
	assert.Expect(err).NotTo(HaveOccurred())
	assert.Expect(body["issuer"]).To(Equal("https://ci.example.com"))
	assert.Expect(body["authorization_endpoint"]).To(Equal("https://ci.example.com/oauth/authorize"))
	assert.Expect(body["token_endpoint"]).To(Equal("https://ci.example.com/oauth/token"))
	assert.Expect(body["registration_endpoint"]).To(Equal("https://ci.example.com/oauth/register"))
	assert.Expect(body["code_challenge_methods_supported"]).To(ContainElement("S256"))
}

func TestOAuthServer(t *testing.T) {
	t.Parallel()
	secret := "test-secret-key-at-least-32-bytes-long"
	cfg := &auth.Config{
		SessionSecret: secret,
		CallbackURL:   "https://ci.example.com",
	}

	store := sessions.NewCookieStore([]byte(secret))
	store.Options = &sessions.Options{
		Path:     "/",
		MaxAge:   86400,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	testUser := &auth.User{
		Email:    "alice@example.com",
		Name:     "Alice",
		NickName: "alice",
		Provider: "github",
		UserID:   "12345",
	}

	// registerTestClient registers a dynamic OAuth client via HandleRegister
	// and returns the assigned client_id.
	registerTestClient := func(t *testing.T, srv *auth.OAuthServer, redirectURIs ...string) string {
		t.Helper()

		if len(redirectURIs) == 0 {
			redirectURIs = []string{"http://localhost/callback"}
		}

		uris, _ := json.Marshal(redirectURIs)
		body := `{"redirect_uris":` + string(uris) + `,"client_name":"test"}`
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.HandleRegister(rec, req)

		if rec.Code != http.StatusCreated {
			t.Fatalf("register failed: %d %s", rec.Code, rec.Body.String())
		}

		var resp map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &resp)

		return resp["client_id"].(string)
	}

	t.Run("authorize without session redirects to login", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback&code_challenge=abc123&code_challenge_method=S256&state=xyz",
			nil)
		rec := httptest.NewRecorder()

		srv.HandleAuthorize(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusFound))
		assert.Expect(rec.Header().Get("Location")).To(ContainSubstring("/auth/login"))
	})

	t.Run("authorize with session issues code", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback&code_challenge=abc123&code_challenge_method=S256&state=xyz",
			nil)
		rec := httptest.NewRecorder()

		err := auth.SaveUserToSession(rec, req, store, testUser)
		assert.Expect(err).NotTo(HaveOccurred())

		cookies := rec.Result().Cookies()
		req2 := httptest.NewRequest(http.MethodGet, req.URL.String(), nil)

		for _, c := range cookies {
			req2.AddCookie(c)
		}

		rec2 := httptest.NewRecorder()

		srv.HandleAuthorize(rec2, req2)

		assert.Expect(rec2.Code).To(Equal(http.StatusFound))

		location := rec2.Header().Get("Location")
		assert.Expect(location).To(ContainSubstring("http://localhost/callback?code="))
		assert.Expect(location).To(ContainSubstring("state=xyz"))
	})

	t.Run("authorize rejects missing code_challenge", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback",
			nil)
		rec := httptest.NewRecorder()

		srv.HandleAuthorize(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))

		var body map[string]string
		err := json.Unmarshal(rec.Body.Bytes(), &body)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(body["error"]).To(Equal("invalid_request"))
	})

	t.Run("full authorization code flow with PKCE", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		codeVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		h := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback&code_challenge="+codeChallenge+"&code_challenge_method=S256&state=mystate",
			nil)
		rec := httptest.NewRecorder()

		err := auth.SaveUserToSession(rec, req, store, testUser)
		assert.Expect(err).NotTo(HaveOccurred())

		cookies := rec.Result().Cookies()
		req2 := httptest.NewRequest(http.MethodGet, req.URL.String(), nil)

		for _, c := range cookies {
			req2.AddCookie(c)
		}

		rec2 := httptest.NewRecorder()

		srv.HandleAuthorize(rec2, req2)
		assert.Expect(rec2.Code).To(Equal(http.StatusFound))

		location, err := url.Parse(rec2.Header().Get("Location"))
		assert.Expect(err).NotTo(HaveOccurred())

		authCode := location.Query().Get("code")
		assert.Expect(authCode).NotTo(BeEmpty())
		assert.Expect(location.Query().Get("state")).To(Equal("mystate"))

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {authCode},
			"redirect_uri":  {"http://localhost/callback"},
			"code_verifier": {codeVerifier},
			"client_id":     {clientID},
		}

		tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		tokenRec := httptest.NewRecorder()

		srv.HandleToken(tokenRec, tokenReq)
		assert.Expect(tokenRec.Code).To(Equal(http.StatusOK))

		var tokenResp map[string]any
		err = json.Unmarshal(tokenRec.Body.Bytes(), &tokenResp)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(tokenResp["access_token"]).NotTo(BeEmpty())
		assert.Expect(tokenResp["token_type"]).To(Equal("Bearer"))
		assert.Expect(tokenResp["scope"]).To(Equal("ci:read"))

		accessToken := tokenResp["access_token"].(string)
		verifier := auth.MCPTokenVerifier(secret)

		info, err := verifier(t.Context(), accessToken, nil)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(info.Scopes).To(ConsistOf("ci:read"))
		assert.Expect(info.UserID).To(Equal("12345"))
	})

	t.Run("token exchange with wrong code_verifier fails", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		codeVerifier := "correct-verifier-value-that-is-at-least-43-chars"
		h := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback&code_challenge="+codeChallenge+"&code_challenge_method=S256",
			nil)
		rec := httptest.NewRecorder()

		err := auth.SaveUserToSession(rec, req, store, testUser)
		assert.Expect(err).NotTo(HaveOccurred())

		cookies := rec.Result().Cookies()
		req2 := httptest.NewRequest(http.MethodGet, req.URL.String(), nil)

		for _, c := range cookies {
			req2.AddCookie(c)
		}

		rec2 := httptest.NewRecorder()

		srv.HandleAuthorize(rec2, req2)

		location, _ := url.Parse(rec2.Header().Get("Location"))
		authCode := location.Query().Get("code")

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {authCode},
			"redirect_uri":  {"http://localhost/callback"},
			"code_verifier": {"wrong-verifier-value"},
			"client_id":     {clientID},
		}

		tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		tokenRec := httptest.NewRecorder()

		srv.HandleToken(tokenRec, tokenReq)
		assert.Expect(tokenRec.Code).To(Equal(http.StatusBadRequest))

		var body map[string]string
		err = json.Unmarshal(tokenRec.Body.Bytes(), &body)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(body["error"]).To(Equal("invalid_grant"))
	})

	t.Run("token exchange with unknown code fails", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {"nonexistent-code"},
			"redirect_uri":  {"http://localhost/callback"},
			"code_verifier": {"some-verifier"},
			"client_id":     {"test"},
		}

		tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		tokenRec := httptest.NewRecorder()

		srv.HandleToken(tokenRec, tokenReq)
		assert.Expect(tokenRec.Code).To(Equal(http.StatusBadRequest))

		var body map[string]string
		err := json.Unmarshal(tokenRec.Body.Bytes(), &body)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(body["error"]).To(Equal("invalid_grant"))
	})

	t.Run("code reuse fails", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		codeVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		h := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback&code_challenge="+codeChallenge+"&code_challenge_method=S256",
			nil)
		rec := httptest.NewRecorder()

		err := auth.SaveUserToSession(rec, req, store, testUser)
		assert.Expect(err).NotTo(HaveOccurred())

		cookies := rec.Result().Cookies()
		req2 := httptest.NewRequest(http.MethodGet, req.URL.String(), nil)

		for _, c := range cookies {
			req2.AddCookie(c)
		}

		rec2 := httptest.NewRecorder()

		srv.HandleAuthorize(rec2, req2)

		location, _ := url.Parse(rec2.Header().Get("Location"))
		authCode := location.Query().Get("code")

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {authCode},
			"redirect_uri":  {"http://localhost/callback"},
			"code_verifier": {codeVerifier},
			"client_id":     {clientID},
		}

		tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		tokenRec := httptest.NewRecorder()

		srv.HandleToken(tokenRec, tokenReq)
		assert.Expect(tokenRec.Code).To(Equal(http.StatusOK))

		tokenReq2 := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		tokenReq2.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		tokenRec2 := httptest.NewRecorder()

		srv.HandleToken(tokenRec2, tokenReq2)
		assert.Expect(tokenRec2.Code).To(Equal(http.StatusBadRequest))
	})

	t.Run("redirect_uri mismatch fails", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv)

		codeVerifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
		h := sha256.Sum256([]byte(codeVerifier))
		codeChallenge := base64.RawURLEncoding.EncodeToString(h[:])

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://localhost/callback&code_challenge="+codeChallenge+"&code_challenge_method=S256",
			nil)
		rec := httptest.NewRecorder()

		err := auth.SaveUserToSession(rec, req, store, testUser)
		assert.Expect(err).NotTo(HaveOccurred())

		cookies := rec.Result().Cookies()
		req2 := httptest.NewRequest(http.MethodGet, req.URL.String(), nil)

		for _, c := range cookies {
			req2.AddCookie(c)
		}

		rec2 := httptest.NewRecorder()

		srv.HandleAuthorize(rec2, req2)

		location, _ := url.Parse(rec2.Header().Get("Location"))
		authCode := location.Query().Get("code")

		form := url.Values{
			"grant_type":    {"authorization_code"},
			"code":          {authCode},
			"redirect_uri":  {"http://evil.com/callback"},
			"code_verifier": {codeVerifier},
			"client_id":     {clientID},
		}

		tokenReq := httptest.NewRequest(http.MethodPost, "/oauth/token", strings.NewReader(form.Encode()))
		tokenReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		tokenRec := httptest.NewRecorder()

		srv.HandleToken(tokenRec, tokenReq)
		assert.Expect(tokenRec.Code).To(Equal(http.StatusBadRequest))

		var body map[string]string
		err = json.Unmarshal(tokenRec.Body.Bytes(), &body)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(body["error"]).To(Equal("invalid_grant"))
	})

	t.Run("dynamic client registration", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())

		body := `{"redirect_uris":["http://127.0.0.1:12345","https://vscode.dev/redirect"],"client_name":"VS Code"}`
		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		srv.HandleRegister(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusCreated))

		var resp map[string]any
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp["client_id"]).NotTo(BeEmpty())
		assert.Expect(resp["client_name"]).To(Equal("VS Code"))
		assert.Expect(resp["redirect_uris"]).To(ConsistOf("http://127.0.0.1:12345", "https://vscode.dev/redirect"))
		assert.Expect(resp["token_endpoint_auth_method"]).To(Equal("none"))
		assert.Expect(resp["client_id_issued_at"]).NotTo(BeZero())
	})

	t.Run("registration rejects missing redirect_uris", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())

		req := httptest.NewRequest(http.MethodPost, "/oauth/register", strings.NewReader(`{"client_name":"test"}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()

		srv.HandleRegister(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))
	})

	t.Run("authorize rejects unregistered client_id", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id=unregistered&redirect_uri=http://localhost/callback&code_challenge=abc&code_challenge_method=S256",
			nil)
		rec := httptest.NewRecorder()

		srv.HandleAuthorize(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusUnauthorized))

		var resp map[string]string
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp["error"]).To(Equal("invalid_client"))
	})

	t.Run("authorize rejects unregistered redirect_uri", func(t *testing.T) {
		assert := NewGomegaWithT(t)

		srv := auth.NewOAuthServer(cfg, store, slogDiscard())
		clientID := registerTestClient(t, srv, "http://localhost/callback")

		req := httptest.NewRequest(http.MethodGet,
			"/oauth/authorize?response_type=code&client_id="+clientID+"&redirect_uri=http://evil.com/steal&code_challenge=abc&code_challenge_method=S256",
			nil)
		rec := httptest.NewRecorder()

		srv.HandleAuthorize(rec, req)

		assert.Expect(rec.Code).To(Equal(http.StatusBadRequest))

		var resp map[string]string
		err := json.Unmarshal(rec.Body.Bytes(), &resp)
		assert.Expect(err).NotTo(HaveOccurred())
		assert.Expect(resp["error"]).To(Equal("invalid_request"))
	})
}

func slogDiscard() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}
