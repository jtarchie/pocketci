package smtp

import (
	"errors"
	"net"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/nikoksr/notify"
	"github.com/nikoksr/notify/service/mail"
)

// Type is the identifier used in NotifyConfig.Type.
const Type = "smtp"

type adapter struct{}

// New returns the SMTP email notification adapter.
func New() jsapi.Adapter {
	return &adapter{}
}

func (a *adapter) Name() string { return Type }

func (a *adapter) Configure(n *notify.Notify, config jsapi.NotifyConfig) error {
	if config.SMTPHost == "" {
		return errors.New("smtp: smtpHost is required (e.g. \"smtp.example.com:587\")")
	}

	if config.From == "" {
		return errors.New("smtp: from address is required")
	}

	svc := mail.New(config.From, config.SMTPHost)

	if config.Username != "" {
		host, _, _ := net.SplitHostPort(config.SMTPHost)
		svc.AuthenticateSMTP("", config.Username, config.Token, host)
	}

	svc.AddReceivers(config.Recipients...)
	n.UseServices(svc)

	return nil
}
