package commands

import (
	"net/url"
	"strings"

	"github.com/jtarchie/pocketci/client"
)

// ServerConfig holds the common connection/auth fields shared by all
// pipeline subcommands that talk to a remote CI server.
type ServerConfig struct {
	ServerURL  string `env:"CI_SERVER_URL"  help:"URL of the CI server"                                        required:"" short:"s"`
	AuthToken  string `env:"CI_AUTH_TOKEN"  help:"Bearer token for OAuth-authenticated servers"                short:"t"`
	ConfigFile string `env:"CI_AUTH_CONFIG" help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
}

// NewClient creates an authenticated API client from the server config.
func (cfg ServerConfig) NewClient(opts ...client.Option) *client.Client {
	serverURL := strings.TrimSuffix(cfg.ServerURL, "/")

	var allOpts []client.Option

	if parsed, err := url.Parse(serverURL); err == nil && parsed.User != nil {
		password, _ := parsed.User.Password()
		allOpts = append(allOpts, client.WithBasicAuth(parsed.User.Username(), password))
		parsed.User = nil
		serverURL = parsed.String()
	}

	token := ResolveAuthToken(cfg.AuthToken, cfg.ConfigFile, serverURL)
	if token != "" {
		allOpts = append(allOpts, client.WithAuthToken(token))
	}

	allOpts = append(allOpts, opts...)

	return client.New(serverURL, allOpts...)
}
