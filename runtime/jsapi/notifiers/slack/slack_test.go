package slack_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/jsapi/notifiers/slack"
	"github.com/nikoksr/notify"
	. "github.com/onsi/gomega"
)

func TestSlackMissingToken(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := slack.New().Configure(notify.New(), jsapi.NotifyConfig{})
	assert.Expect(err).To(MatchError(ContainSubstring("slack token is required")))
}

func TestSlackName(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)
	assert.Expect(slack.New().Name()).To(Equal("slack"))
}
