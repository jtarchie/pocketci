package teams_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/jsapi/notifiers/teams"
	"github.com/nikoksr/notify"
	. "github.com/onsi/gomega"
)

func TestTeamsMissingWebhook(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := teams.New().Configure(notify.New(), jsapi.NotifyConfig{})
	assert.Expect(err).To(MatchError(ContainSubstring("teams webhook URL is required")))
}

func TestTeamsName(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)
	assert.Expect(teams.New().Name()).To(Equal("teams"))
}
