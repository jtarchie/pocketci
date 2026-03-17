package server_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/jtarchie/pocketci/orchestra"
	"github.com/jtarchie/pocketci/orchestra/native"
	"github.com/jtarchie/pocketci/secrets"
	secretssqlite "github.com/jtarchie/pocketci/secrets/sqlite"
	"github.com/jtarchie/pocketci/server"
	"github.com/jtarchie/pocketci/storage"
	"github.com/jtarchie/pocketci/webhooks"
	webhookgeneric "github.com/jtarchie/pocketci/webhooks/generic"
	webhookgithub "github.com/jtarchie/pocketci/webhooks/github"
	webhookhoneybadger "github.com/jtarchie/pocketci/webhooks/honeybadger"
	webhookslack "github.com/jtarchie/pocketci/webhooks/slack"
)

func newStrictSecretRouter(t *testing.T, client storage.Driver, opts server.RouterOptions) *server.Router {
	t.Helper()

	if opts.DriverFactory == nil {
		opts.DriverFactory = func(ns string) (orchestra.Driver, error) {
			return native.New(native.Config{Namespace: ns}, slog.Default())
		}
	}

	if opts.WebhookProviders == nil {
		opts.WebhookProviders = []webhooks.Provider{
			webhookgithub.New(),
			webhookhoneybadger.New(),
			webhookslack.New(),
			webhookgeneric.New(),
		}
	}

	if opts.SecretsManager == nil {
		secretsMgr, err := secretssqlite.New(secretssqlite.Config{Path: ":memory:", Passphrase: "test-key"}, slog.Default())
		if err != nil {
			t.Fatalf("could not create secrets manager: %v", err)
		}

		t.Cleanup(func() { _ = secretsMgr.Close() })
		opts.SecretsManager = secretsMgr
	}

	router, err := server.NewRouter(slog.Default(), client, opts)
	if err != nil {
		t.Fatalf("could not create router: %v", err)
	}

	result, err := client.SearchPipelines(context.Background(), "", 1, 1000)
	if err != nil {
		t.Fatalf("could not seed pipeline driver secrets: %v", err)
	}

	for i := range result.Items {
		pipeline := result.Items[i]
		if pipeline.DriverDSN == "" {
			continue
		}

		persistPipelineDriverDSNSecret(t, opts.SecretsManager, pipeline.ID, pipeline.DriverDSN)
	}

	return router
}

func persistPipelineDriverDSNSecret(t *testing.T, mgr secrets.Manager, pipelineID string, driverDSN string) {
	t.Helper()

	if err := mgr.Set(context.Background(), secrets.PipelineScope(pipelineID), "driver_dsn", driverDSN); err != nil {
		t.Fatalf("could not persist pipeline driver secret: %v", err)
	}
}
