package server

import (
	"context"
	"log/slog"

	"github.com/jtarchie/pocketci/server/auth"
)

// RequestActorFromContext reads authenticated actor info from request context.
func RequestActorFromContext(ctx context.Context) (auth.RequestActor, bool) {
	return auth.RequestActorFromContext(ctx)
}

// LoggerWithRequestActor enriches logger with authenticated actor attributes.
func LoggerWithRequestActor(logger *slog.Logger, ctx context.Context) *slog.Logger {
	if actor, ok := RequestActorFromContext(ctx); ok {
		return logger.With("auth_provider", actor.Provider, "user", actor.User)
	}

	return logger
}
