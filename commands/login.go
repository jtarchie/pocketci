package commands

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/jtarchie/pocketci/client"
)

type Login struct {
	ServerURL  string `env:"CI_SERVER_URL"  help:"URL of the CI server"                                        required:"" short:"s"`
	ConfigFile string `env:"CI_AUTH_CONFIG" help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
}

func (c *Login) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("login")

	serverURL := strings.TrimSuffix(c.ServerURL, "/")
	apiClient := client.New(serverURL)

	code, err := c.beginDeviceFlow(apiClient, logger)
	if err != nil {
		return err
	}

	return c.pollForToken(apiClient, logger, serverURL, code)
}

func (c *Login) beginDeviceFlow(apiClient *client.Client, logger *slog.Logger) (string, error) {
	logger.Info("login.begin", "server", apiClient.ServerURL())

	result, err := apiClient.BeginDeviceFlow()
	if err != nil {
		return "", err
	}

	approveURL := apiClient.ServerURL() + "/auth/cli/approve?code=" + url.QueryEscape(result.Code)

	fmt.Println("Opening browser for authentication...")
	fmt.Printf("If the browser does not open, visit:\n  %s\n\n", approveURL)
	fmt.Printf("Your device code: %s\n\n", result.Code)

	openBrowser(approveURL)

	return result.Code, nil
}

func (c *Login) pollForToken(apiClient *client.Client, logger *slog.Logger, serverURL, code string) error {
	fmt.Print("Waiting for authentication...")

	timeout := time.After(10 * time.Minute)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			fmt.Println()
			return errors.New("authentication timed out after 10 minutes")
		case <-ticker.C:
			fmt.Print(".")

			token, done, err := apiClient.PollDeviceFlow(code)
			if err != nil {
				return err
			}

			if !done {
				continue
			}

			fmt.Println(" authenticated!")
			c.saveToken(logger, serverURL, token)
			fmt.Printf("\nexport CI_AUTH_TOKEN=%s\n", token)

			return nil
		}
	}
}

func (c *Login) saveToken(logger *slog.Logger, serverURL, token string) {
	cfgPath := c.ConfigFile
	if cfgPath == "" {
		var pathErr error

		cfgPath, pathErr = defaultConfigPath()
		if pathErr != nil {
			logger.Warn("login.config_path.failed", "error", pathErr)
		}
	}

	if cfgPath == "" {
		return
	}

	cfg, loadErr := LoadAuthConfig(cfgPath)
	if loadErr != nil {
		logger.Warn("login.load_config.failed", "error", loadErr)
		cfg = &AuthConfig{Servers: make(map[string]AuthEntry)}
	}

	cfg.Servers[normalizeServerURL(serverURL)] = AuthEntry{Token: token}

	if saveErr := SaveAuthConfig(cfgPath, cfg); saveErr != nil {
		logger.Warn("login.save_token.failed", "error", saveErr)
		fmt.Printf("\nCould not save token to config file: %v\n", saveErr)
		fmt.Println("You can set it manually:")
	} else {
		fmt.Printf("\nToken saved to %s\n", cfgPath)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}

	_ = cmd.Start()
}
