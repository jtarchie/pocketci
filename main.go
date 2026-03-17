package main

import (
	"log/slog"
	"os"

	"github.com/alecthomas/kong"
	"github.com/jtarchie/pocketci/commands"
	_ "github.com/jtarchie/pocketci/orchestra/cache/s3"
	_ "github.com/jtarchie/pocketci/orchestra/digitalocean"
	_ "github.com/jtarchie/pocketci/orchestra/docker"
	_ "github.com/jtarchie/pocketci/orchestra/fly"
	_ "github.com/jtarchie/pocketci/orchestra/k8s"
	_ "github.com/jtarchie/pocketci/orchestra/native"
	_ "github.com/jtarchie/pocketci/orchestra/qemu"
	_ "github.com/jtarchie/pocketci/resources/mock"
	_ "github.com/jtarchie/pocketci/secrets/s3"
	_ "github.com/jtarchie/pocketci/secrets/sqlite"
	_ "github.com/jtarchie/pocketci/storage/s3"
	_ "github.com/jtarchie/pocketci/storage/sqlite"
	_ "github.com/jtarchie/pocketci/webhooks/generic"
	_ "github.com/jtarchie/pocketci/webhooks/github"
	_ "github.com/jtarchie/pocketci/webhooks/honeybadger"
	_ "github.com/jtarchie/pocketci/webhooks/slack"
	"github.com/lmittmann/tint"
)

type CLI struct {
	Run            commands.Run            `cmd:"" help:"Run a stored pipeline by name on a server"`
	Resource       commands.Resource       `cmd:"" help:"Execute a native resource operation"`
	Server         commands.Server         `cmd:"" help:"Run a server"`
	SetPipeline    commands.SetPipeline    `cmd:"" help:"Upload a pipeline to the server"  name:"set-pipeline"`
	DeletePipeline commands.DeletePipeline `cmd:"" help:"Delete a pipeline from the server" name:"delete-pipeline"`
	Login          commands.Login          `cmd:"" help:"Authenticate with a CI server via browser-based OAuth"`

	LogLevel  slog.Level `default:"info"             env:"CI_LOG_LEVEL"   help:"Set the log level (debug, info, warn, error)"`
	AddSource bool       `env:"CI_ADD_SOURCE"        help:"Add source code location to log messages"`
	LogFormat string     `default:"text"             env:"CI_LOG_FORMAT"  enum:"text,json" help:"Set the log format (text, json)"`
}

func main() {
	cli := &CLI{}
	ctx := kong.Parse(cli)

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
