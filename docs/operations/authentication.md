# Authentication

PocketCI supports two mutually exclusive authentication strategies: **Basic
Auth** and **OAuth**. You cannot enable both simultaneously.

If neither is configured, the server runs in open-access mode — all endpoints
are accessible without credentials.

## Basic Auth

The simplest option. Set a username and password on the server:

```bash
pocketci server \
  --basic-auth-username admin \
  --basic-auth-password secret123
```

| Flag / Env Var          | Description                              |
| ----------------------- | ---------------------------------------- |
| `--basic-auth-username` | Username (env: `CI_BASIC_AUTH_USERNAME`) |
| `--basic-auth-password` | Password (env: `CI_BASIC_AUTH_PASSWORD`) |

CLI commands embed credentials in the server URL:

```bash
pocketci set-pipeline my-pipeline.ts \
  --server http://admin:secret123@localhost:8080
```

> **Limitation:** Basic auth does not support pipeline-level RBAC expressions.
> RBAC requires OAuth so that user attributes (email, organizations, groups) can
> be evaluated. See [Authorization (RBAC)](./rbac.md) for details.

## OAuth

OAuth delegates authentication to an external identity provider (GitHub, GitLab,
or Microsoft). Users log in via browser redirect; CLI tools use a device-flow
login.

### Supported Providers

| Provider  | Client ID Flag                | Client Secret Flag                | Extra Flags                            |
| --------- | ----------------------------- | --------------------------------- | -------------------------------------- |
| GitHub    | `--oauth-github-client-id`    | `--oauth-github-client-secret`    | —                                      |
| GitLab    | `--oauth-gitlab-client-id`    | `--oauth-gitlab-client-secret`    | `--oauth-gitlab-url` (for self-hosted) |
| Microsoft | `--oauth-microsoft-client-id` | `--oauth-microsoft-client-secret` | `--oauth-microsoft-tenant`             |

Both client ID and client secret must be set to enable a provider. You can
enable multiple providers simultaneously — users choose at the login screen.

### Required Flags

| Flag                     | Description                                                                                                    |
| ------------------------ | -------------------------------------------------------------------------------------------------------------- |
| `--oauth-session-secret` | Secret key for encrypting session cookies and signing JWT tokens. Use a strong random string (32+ characters). |
| `--oauth-callback-url`   | Full public URL for OAuth callbacks (e.g., `https://ci.example.com`).                                          |

### Provider Callback URLs

The `--oauth-callback-url` flag is your server's **base URL** (no trailing
path). PocketCI automatically appends the provider-specific callback path. When
registering your OAuth application with each provider, use the corresponding
full callback URL:

| Provider  | Callback URL to register                               |
| --------- | ------------------------------------------------------ |
| GitHub    | `https://ci.example.com/auth/github/callback`          |
| GitLab    | `https://ci.example.com/auth/gitlab/callback`          |
| Microsoft | `https://ci.example.com/auth/microsoftonline/callback` |

Replace `https://ci.example.com` with your actual `--oauth-callback-url` value.

### Example

```bash
pocketci server \
  --oauth-github-client-id GITHUB_CLIENT_ID \
  --oauth-github-client-secret GITHUB_CLIENT_SECRET \
  --oauth-session-secret "$(openssl rand -hex 32)" \
  --oauth-callback-url https://ci.example.com
```

All OAuth flags have corresponding environment variables prefixed with `CI_`:

```bash
export CI_OAUTH_GITHUB_CLIENT_ID=...
export CI_OAUTH_GITHUB_CLIENT_SECRET=...
export CI_OAUTH_SESSION_SECRET="$(openssl rand -hex 32)"
export CI_OAUTH_CALLBACK_URL=https://ci.example.com

pocketci server
```

### GitHub Organization Enrichment

When using the GitHub provider, PocketCI automatically fetches the authenticated
user's organization memberships via the GitHub API. These are available in RBAC
expressions as the `Organizations` field.

### GitLab Self-Hosted

Point GitLab to your instance with `--oauth-gitlab-url`:

```bash
pocketci server \
  --oauth-gitlab-client-id ... \
  --oauth-gitlab-client-secret ... \
  --oauth-gitlab-url https://gitlab.company.com \
  --oauth-session-secret ... \
  --oauth-callback-url https://ci.example.com
```

## JWT Tokens

When OAuth is enabled, PocketCI issues JWT tokens (HS256) for CLI and API
authentication. Tokens contain:

| Claim      | JWT Field   | Description               |
| ---------- | ----------- | ------------------------- |
| Subject    | `sub`       | User ID from the provider |
| Email      | `email`     | User's email address      |
| Name       | `name`      | Display name              |
| Nickname   | `nick_name` | Provider username         |
| Provider   | `provider`  | OAuth provider name       |
| Issuer     | `iss`       | Always `pocketci`         |
| Issued At  | `iat`       | Token creation timestamp  |
| Expires At | `exp`       | Token expiry (30 days)    |

Tokens are signed with the `--oauth-session-secret` key using HMAC-SHA256.

### Using Tokens

Pass tokens via the `Authorization` header:

```bash
curl -H "Authorization: Bearer <token>" http://localhost:8080/api/pipelines
```

Or via environment variable for CLI commands:

```bash
export CI_AUTH_TOKEN=<token>
pocketci run my-pipeline -s http://localhost:8080
```

## CLI Login

The `pocketci login` command authenticates via a browser-based device flow:

```bash
pocketci login -s https://ci.example.com
```

This opens a browser, completes OAuth, and saves the token to
`~/.pocketci/auth.config`. Subsequent CLI commands automatically use the saved
token for that server.

### Auth Config File

Tokens are stored in `~/.pocketci/auth.config` (JSON, `0600` permissions):

```json
{
  "servers": {
    "https://ci.example.com": {
      "token": "eyJhbGciOiJIUzI1NiIs..."
    }
  }
}
```

Override the config file location:

```bash
pocketci login -s https://ci.example.com -c /path/to/auth.config
# or
export CI_AUTH_CONFIG=/path/to/auth.config
```

### Token Resolution Order

CLI commands resolve auth tokens in this order:

1. `--auth-token` flag / `CI_AUTH_TOKEN` env var (highest priority)
2. Config file lookup by server URL (`~/.pocketci/auth.config`)
3. Basic auth credentials embedded in the server URL

### Auth Error Messages

When a server requires authentication, CLI commands return a helpful message:

```
authentication required: server https://ci.example.com returned 401 Unauthorized

Please log in first:
  ci login -s https://ci.example.com

Or provide a token directly:
  export CI_AUTH_TOKEN=<token>
```

## MCP Clients

MCP clients (VS Code, Claude, etc.) authenticate with the same mechanisms as the
REST API. With OAuth enabled, use a Bearer token:

```jsonc
// .vscode/mcp.json
{
  "servers": {
    "ci": {
      "url": "https://ci.example.com/mcp",
      "type": "http",
      "headers": {
        "Authorization": "Bearer ${input:ciToken}"
      }
    }
  },
  "inputs": [
    {
      "id": "ciToken",
      "type": "promptString",
      "description": "PocketCI auth token (from: pocketci login)",
      "password": true
    }
  ]
}
```

With basic auth, use the Basic scheme instead:

```jsonc
{
  "servers": {
    "ci": {
      "url": "http://localhost:8080/mcp",
      "type": "http",
      "headers": {
        "Authorization": "Basic ${input:ciBasicAuth}"
      }
    }
  }
}
```
