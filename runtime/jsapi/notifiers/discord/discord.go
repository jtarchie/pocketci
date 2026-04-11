package discord

import (
	"errors"
	"fmt"

	"github.com/jtarchie/pocketci/runtime/jsapi"
	"github.com/nikoksr/notify"
	ndiscord "github.com/nikoksr/notify/service/discord"
)

// Type is the identifier used in NotifyConfig.Type.
const Type = "discord"

type adapter struct{}

// New returns the Discord notification adapter.
func New() jsapi.Adapter {
	return &adapter{}
}

func (a *adapter) Name() string { return Type }

func (a *adapter) Configure(n *notify.Notify, config jsapi.NotifyConfig) error {
	if config.Token == "" {
		return errors.New("discord bot token is required")
	}

	svc := ndiscord.New()

	err := svc.AuthenticateWithBotToken(config.Token)
	if err != nil {
		return fmt.Errorf("discord: authenticate with bot token: %w", err)
	}

	svc.AddReceivers(config.Channels...)
	svc.AddReceivers(config.Recipients...)
	n.UseServices(svc)

	return nil
}
