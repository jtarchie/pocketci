package server

import (
	"context"
	"log/slog"
)

const requestIDKey = contextKey("request_id")

type contextKey string

// RequestIDFromContext extracts request_id from context when available.
func RequestIDFromContext(ctx context.Context) (string, bool) {
	rid, ok := ctx.Value(requestIDKey).(string)
	if !ok || rid == "" {
		return "", false
	}

	return rid, true
}

// LoggerWithRequestID enriches logger with request_id from context.
func LoggerWithRequestID(logger *slog.Logger, ctx context.Context) *slog.Logger {
	if rid, ok := RequestIDFromContext(ctx); ok {
		return logger.With("request_id", rid)
	}

	return logger
}

func requestContextWithRequestID(ctx context.Context, requestID string) context.Context {
	if requestID == "" {
		return ctx
	}

	return context.WithValue(ctx, requestIDKey, requestID)
}
