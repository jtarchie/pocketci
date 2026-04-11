package teams

import (
	"errors"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/nikoksr/notify"
	"github.com/nikoksr/notify/service/msteams"
)

// Type is the identifier used in NotifyConfig.Type.
const Type = "teams"

type adapter struct{}

// New returns the Microsoft Teams notification adapter.
func New() jsapi.Adapter {
	return &adapter{}
}

func (a *adapter) Name() string { return Type }

func (a *adapter) Configure(n *notify.Notify, config jsapi.NotifyConfig) error {
	if config.Webhook == "" {
		return errors.New("teams webhook URL is required")
	}

	svc := msteams.New()
	svc.AddReceivers(config.Webhook)
	n.UseServices(svc)

	return nil
}
