package smtp_test

import (
	"testing"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/jtarchie/pocketci/runtime/jsapi/notifiers/smtp"
	"github.com/nikoksr/notify"
	. "github.com/onsi/gomega"
)

func TestSMTPMissingHost(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := smtp.New().Configure(notify.New(), jsapi.NotifyConfig{From: "ci@example.com"})
	assert.Expect(err).To(MatchError(ContainSubstring("smtpHost is required")))
}

func TestSMTPMissingFrom(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)

	err := smtp.New().Configure(notify.New(), jsapi.NotifyConfig{SMTPHost: "smtp.example.com:587"})
	assert.Expect(err).To(MatchError(ContainSubstring("from address is required")))
}

func TestSMTPName(t *testing.T) {
	t.Parallel()

	assert := NewGomegaWithT(t)
	assert.Expect(smtp.New().Name()).To(Equal("smtp"))
}
