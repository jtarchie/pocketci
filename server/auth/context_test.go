package auth_test

import (
	"context"
	"testing"

	"github.com/jtarchie/pocketci/server/auth"
	. "github.com/onsi/gomega"
)

func TestRequestActorContext(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	ctx := auth.WithRequestActor(context.Background(), auth.RequestActor{Provider: "github", User: "alice@example.com"})
	actor, ok := auth.RequestActorFromContext(ctx)
	assert.Expect(ok).To(BeTrue())
	assert.Expect(actor.Provider).To(Equal("github"))
	assert.Expect(actor.User).To(Equal("alice@example.com"))
}

func TestRequestActorContextIgnoresIncompleteActor(t *testing.T) {
	t.Parallel()
	assert := NewGomegaWithT(t)

	ctx := auth.WithRequestActor(context.Background(), auth.RequestActor{Provider: "github"})
	_, ok := auth.RequestActorFromContext(ctx)
	assert.Expect(ok).To(BeFalse())
}
