package slack

import (
	"errors"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/nikoksr/notify"
	nslack "github.com/nikoksr/notify/service/slack"
)

// Type is the identifier used in NotifyConfig.Type.
const Type = "slack"

type adapter struct{}

// New returns the Slack notification adapter.
func New() jsapi.Adapter {
	return &adapter{}
}

func (a *adapter) Name() string { return Type }

func (a *adapter) Configure(n *notify.Notify, config jsapi.NotifyConfig) error {
	if config.Token == "" {
		return errors.New("slack token is required")
	}

	svc := nslack.New(config.Token)
	svc.AddReceivers(config.Channels...)
	svc.AddReceivers(config.Recipients...)
	n.UseServices(svc)

	return nil
}
