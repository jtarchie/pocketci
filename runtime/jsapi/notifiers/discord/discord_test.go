package discord_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/jsapi/notifiers/discord"
	"github.com/nikoksr/notify"
	. "github.com/onsi/gomega"
)

func TestDiscordMissingToken(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := discord.New().Configure(notify.New(), jsapi.NotifyConfig{})
	assert.Expect(err).To(MatchError(ContainSubstring("discord bot token is required")))
}

func TestDiscordName(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)
	assert.Expect(discord.New().Name()).To(Equal("discord"))
}
