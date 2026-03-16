package auth

import "context"

type requestActorContextKey string

const actorContextKey requestActorContextKey = "auth_actor"

// RequestActor identifies an authenticated caller for request-scoped logging.
type RequestActor struct {
	Provider string
	User     string
}

// WithRequestActor stores an authenticated actor on a request context.
func WithRequestActor(ctx context.Context, actor RequestActor) context.Context {
	if actor.Provider == "" || actor.User == "" {
		return ctx
	}

	return context.WithValue(ctx, actorContextKey, actor)
}

// RequestActorFromContext retrieves an authenticated actor from context.
func RequestActorFromContext(ctx context.Context) (RequestActor, bool) {
	actor, ok := ctx.Value(actorContextKey).(RequestActor)
	if !ok || actor.Provider == "" || actor.User == "" {
		return RequestActor{}, false
	}

	return actor, true
}

func actorFromOAuthUser(user *User) RequestActor {
	if user == nil {
		return RequestActor{}
	}

	identifier := user.Email
	if identifier == "" {
		identifier = user.UserID
	}

	if identifier == "" {
		identifier = user.Name
	}

	if identifier == "" {
		return RequestActor{}
	}

	provider := user.Provider
	if provider == "" {
		provider = "oauth"
	}

	return RequestActor{Provider: provider, User: identifier}
}
