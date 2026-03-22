package commands

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
)

type Login struct {
	ServerURL  string `env:"CI_SERVER_URL"     help:"URL of the CI server" required:"" short:"s"`
	ConfigFile string `env:"CI_AUTH_CONFIG"     help:"Path to auth config file (default: ~/.pocketci/auth.config)" short:"c"`
}

func (c *Login) Run(logger *slog.Logger) error {
	logger = logger.WithGroup("login")

	serverURL := strings.TrimSuffix(c.ServerURL, "/")

	client := resty.New()

	// Step 1: Begin device flow
	logger.Info("login.begin", "server", serverURL)

	beginResp, err := client.R().
		Post(serverURL + "/auth/cli/begin")
	if err != nil {
		return fmt.Errorf("could not connect to server: %w", err)
	}

	if beginResp.StatusCode() != 200 {
		return fmt.Errorf("server error (%d): %s", beginResp.StatusCode(), beginResp.String())
	}

	var beginResult struct {
		Code     string `json:"code"`
		LoginURL string `json:"login_url"`
	}

	if err := json.Unmarshal(beginResp.Body(), &beginResult); err != nil {
		return fmt.Errorf("could not parse response: %w", err)
	}

	approveURL := serverURL + "/auth/cli/approve?code=" + url.QueryEscape(beginResult.Code)

	fmt.Println("Opening browser for authentication...")
	fmt.Printf("If the browser does not open, visit:\n  %s\n\n", approveURL)
	fmt.Printf("Your device code: %s\n\n", beginResult.Code)

	// Try to open browser
	openBrowser(approveURL)

	// Step 2: Poll for completion
	fmt.Print("Waiting for authentication...")

	pollEndpoint := serverURL + "/auth/cli/poll?code=" + url.QueryEscape(beginResult.Code)
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

			pollResp, err := client.R().Get(pollEndpoint)
			if err != nil {
				continue
			}

			if pollResp.StatusCode() == 202 {
				// Still pending
				continue
			}

			if pollResp.StatusCode() == 200 {
				var tokenResult struct {
					Token string `json:"token"`
				}

				if err := json.Unmarshal(pollResp.Body(), &tokenResult); err != nil {
					fmt.Println()
					return fmt.Errorf("could not parse token response: %w", err)
				}

				fmt.Println(" authenticated!")

				// Determine config path
				cfgPath := c.ConfigFile
				if cfgPath == "" {
					var pathErr error

					cfgPath, pathErr = defaultConfigPath()
					if pathErr != nil {
						logger.Warn("login.config_path.failed", "error", pathErr)
					}
				}

				// Save token to config file
				if cfgPath != "" {
					cfg, loadErr := LoadAuthConfig(cfgPath)
					if loadErr != nil {
						logger.Warn("login.load_config.failed", "error", loadErr)
						cfg = &AuthConfig{Servers: make(map[string]AuthEntry)}
					}

					cfg.Servers[normalizeServerURL(serverURL)] = AuthEntry{Token: tokenResult.Token}

					if saveErr := SaveAuthConfig(cfgPath, cfg); saveErr != nil {
						logger.Warn("login.save_token.failed", "error", saveErr)
						fmt.Printf("\nCould not save token to config file: %v\n", saveErr)
						fmt.Println("You can set it manually:")
					} else {
						fmt.Printf("\nToken saved to %s\n", cfgPath)
					}
				}

				fmt.Printf("\nexport CI_AUTH_TOKEN=%s\n", tokenResult.Token)

				return nil
			}

			if pollResp.StatusCode() == 410 {
				fmt.Println()
					return errors.New("authentication code expired, please try again")
			}

			fmt.Println()

			return fmt.Errorf("unexpected response (%d): %s", pollResp.StatusCode(), pollResp.String())
		}
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
