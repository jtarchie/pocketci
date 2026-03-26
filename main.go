package main

import (
	"log/slog"
	"os"

	"github.com/alecthomas/kong"
	"github.com/jtarchie/pocketci/commands"
	_ "github.com/jtarchie/pocketci/resources/mock"
	"github.com/lmittmann/tint"
)

var version = "dev"

type CLI struct {
	Pipeline commands.Pipeline `cmd:"" help:"Manage pipelines"`
	Resource commands.Resource `cmd:"" help:"Execute a native resource operation"`
	Server   commands.Server   `cmd:"" help:"Run a server"`
	Login    commands.Login    `cmd:"" help:"Authenticate with a CI server via browser-based OAuth"`

	LogLevel  slog.Level       `default:"info"        env:"CI_LOG_LEVEL"                              help:"Set the log level (debug, info, warn, error)"`
	AddSource bool             `env:"CI_ADD_SOURCE"   help:"Add source code location to log messages"`
	LogFormat string           `default:"text"        enum:"text,json"                                env:"CI_LOG_FORMAT"                                 help:"Set the log format (text, json)"`
	Version   kong.VersionFlag `help:"Print version." name:"version"                                  short:"V"`
}

func main() {
	cli := &CLI{}
	ctx := kong.Parse(cli, kong.Vars{"version": version})

	if cli.LogFormat == "json" {
		slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{
			Level:     cli.LogLevel,
			AddSource: cli.AddSource,
		})))
	} else {
		slog.SetDefault(slog.New(tint.NewHandler(os.Stderr, &tint.Options{
			Level:     cli.LogLevel,
			AddSource: cli.AddSource,
		})))
	}

	err := ctx.Run(slog.Default())
	ctx.FatalIfErrorf(err)
}
