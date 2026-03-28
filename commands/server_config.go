package commands

// ServerConfig holds the common connection/auth fields shared by all
// pipeline subcommands that talk to a remote CI server.
type ServerConfig struct {
	ServerURL  string `env:"CI_SERVER_URL"  help:"URL of the CI server"                                        required:"" short:"s"`
	AuthToken  string `env:"CI_AUTH_TOKEN"  help:"Bearer token for OAuth-authenticated servers"                short:"t"`
	ConfigFile string `env:"CI_AUTH_CONFIG" help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
}
