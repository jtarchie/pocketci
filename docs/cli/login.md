# pocketci login

Authenticate with a CI server via browser-based OAuth device flow.

```bash
pocketci login --server-url <url> [options]
```

## Options

- `--server-url`, `-s` — CI server URL (required; env: `CI_SERVER_URL`)
- `--config-file`, `-c` — path to auth config file (env: `CI_AUTH_CONFIG`;
  default: `~/.pocketci/auth.config`)

## How It Works

1. The CLI requests a device code from the server
2. A browser window opens for OAuth authentication
3. The CLI polls the server until authentication completes
4. The JWT token is saved to the auth config file

## Example

```bash
pocketci login -s https://ci.example.com
```

Output:

```
Opening browser for authentication...
Your device code: a1b2c3d4...

Waiting for authentication... authenticated!

Token saved to /Users/you/.pocketci/auth.config

export CI_AUTH_TOKEN=eyJhbGciOiJIUzI1NiIs...
```

After login, all CLI commands targeting that server use the saved token
automatically:

```bash
# No --auth-token needed — resolved from ~/.pocketci/auth.config
pocketci pipeline run my-pipeline -s https://ci.example.com
pocketci pipeline set deploy.ts -s https://ci.example.com
pocketci pipeline rm old-pipe -s https://ci.example.com
```

## Multiple Servers

The config file stores tokens per server URL. Log into multiple servers:

```bash
pocketci login -s https://ci-staging.example.com
pocketci login -s https://ci-prod.example.com
```

Each CLI command looks up the token matching its `--server-url`.

See [Authentication](../operations/authentication.md) for full details on token
resolution and auth configuration.
