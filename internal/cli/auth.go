package cli

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/anjor/freeagent-cli/internal/config"
	"github.com/anjor/freeagent-cli/internal/storage"

	"github.com/urfave/cli/v2"
)

const defaultRedirectURI = "http://127.0.0.1:8797/callback"

func authCommand() *cli.Command {
	return &cli.Command{
		Name:  "auth",
		Usage: "Authenticate with FreeAgent",
		Subcommands: []*cli.Command{
			{
				Name:  "configure",
				Usage: "Save OAuth app settings to config",
				Flags: []cli.Flag{
					&cli.StringFlag{
						Name:    "redirect",
						EnvVars: []string{"FREEAGENT_REDIRECT_URI"},
						Usage:   "Override redirect URI",
					},
					&cli.StringFlag{
						Name:    "client-id",
						EnvVars: []string{"FREEAGENT_CLIENT_ID"},
						Usage:   "OAuth client ID",
					},
					&cli.StringFlag{
						Name:    "client-secret",
						EnvVars: []string{"FREEAGENT_CLIENT_SECRET"},
						Usage:   "OAuth client secret",
					},
					&cli.StringFlag{
						Name:    "user-agent",
						EnvVars: []string{"FREEAGENT_USER_AGENT"},
						Usage:   "Custom User-Agent",
					},
				},
				Action: authConfigure,
			},
			{
				Name:  "login",
				Usage: "Start OAuth flow",
				Flags: []cli.Flag{
					&cli.BoolFlag{
						Name:  "manual",
						Usage: "Use manual code paste instead of local callback",
					},
					&cli.StringFlag{
						Name:    "redirect",
						EnvVars: []string{"FREEAGENT_REDIRECT_URI"},
						Usage:   "Override redirect URI",
					},
					&cli.StringFlag{
						Name:    "client-id",
						EnvVars: []string{"FREEAGENT_CLIENT_ID"},
						Usage:   "OAuth client ID",
					},
					&cli.StringFlag{
						Name:    "client-secret",
						EnvVars: []string{"FREEAGENT_CLIENT_SECRET"},
						Usage:   "OAuth client secret",
					},
					&cli.StringFlag{
						Name:    "user-agent",
						EnvVars: []string{"FREEAGENT_USER_AGENT"},
						Usage:   "Custom User-Agent",
					},
					&cli.BoolFlag{
						Name:  "save",
						Value: true,
						Usage: "Save OAuth settings to config",
					},
				},
				Action: authLogin,
			},
			{
				Name:   "status",
				Usage:  "Show current auth status",
				Action: authStatus,
			},
			{
				Name:   "refresh",
				Usage:  "Refresh access token",
				Action: authRefresh,
			},
			{
				Name:   "logout",
				Usage:  "Delete stored tokens",
				Action: authLogout,
			},
		},
	}
}

func authLogin(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}

	cfg, cfgPath, err := loadConfig(rt)
	if err != nil {
		return err
	}

	overrides := config.Profile{
		ClientID:     c.String("client-id"),
		ClientSecret: c.String("client-secret"),
		RedirectURI:  c.String("redirect"),
		UserAgent:    c.String("user-agent"),
	}

	profile := ensureProfile(cfg, rt.Profile, rt, overrides)
	if profile.RedirectURI == "" {
		profile.RedirectURI = defaultRedirectURI
	}

	if !c.Bool("manual") {
		if parsed, err := url.Parse(profile.RedirectURI); err != nil || parsed.Scheme != "http" {
			return fmt.Errorf("redirect uri must be http for local callback (got %s)", profile.RedirectURI)
		}
	}

	if err := require(profile.ClientID, "client-id"); err != nil {
		return err
	}
	if err := require(profile.ClientSecret, "client-secret"); err != nil {
		return err
	}

	if c.Bool("save") {
		if err := saveProfile(cfg, rt.Profile, cfgPath, profile); err != nil {
			return err
		}
	}

	client, store, err := newClient(context.Background(), rt, profile)
	if err != nil {
		return err
	}

	state := randomState()
	authURL, err := client.ApproveURL(state)
	if err != nil {
		return err
	}

	var code string
	if c.Bool("manual") {
		fmt.Fprintf(os.Stdout, "Open this URL in your browser:\n%s\n\nPaste the code or redirected URL: ", authURL)
		reader := bufio.NewReader(os.Stdin)
		text, _ := reader.ReadString('\n')
		code, err = extractCode(strings.TrimSpace(text))
		if err != nil {
			return err
		}
	} else {
		code, err = waitForCallback(authURL, profile.RedirectURI, state)
		if err != nil {
			return err
		}
	}

	token, err := client.ExchangeCode(context.Background(), code)
	if err != nil {
		return err
	}

	if err := store.Set(rt.Profile, token); err != nil {
		return err
	}

	if rt.JSONOutput {
		return writeJSONOutput(mustMarshal(map[string]any{
			"status":     "ok",
			"profile":    rt.Profile,
			"expires_at": token.ExpiresAt,
		}))
	}

	fmt.Fprintf(os.Stdout, "Authenticated profile %s (expires %s)\n", rt.Profile, token.ExpiresAt.Format(time.RFC3339))
	return nil
}

func authConfigure(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}

	cfg, cfgPath, err := loadConfig(rt)
	if err != nil {
		return err
	}

	profile := ensureProfile(cfg, rt.Profile, rt, config.Profile{
		ClientID:     c.String("client-id"),
		ClientSecret: c.String("client-secret"),
		RedirectURI:  c.String("redirect"),
		UserAgent:    c.String("user-agent"),
	})

	if profile.RedirectURI == "" {
		profile.RedirectURI = defaultRedirectURI
	}

	if err := require(profile.ClientID, "client-id"); err != nil {
		return err
	}
	if err := require(profile.ClientSecret, "client-secret"); err != nil {
		return err
	}

	if err := saveProfile(cfg, rt.Profile, cfgPath, profile); err != nil {
		return err
	}

	if rt.JSONOutput {
		return writeJSONOutput(mustMarshal(map[string]any{
			"status":  "ok",
			"profile": rt.Profile,
		}))
	}

	fmt.Fprintf(os.Stdout, "Saved OAuth settings for profile %s\n", rt.Profile)
	return nil
}

func authStatus(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}

	_, _, err = loadConfig(rt)
	if err != nil {
		return err
	}

	store, err := storage.NewDefaultStore()
	if err != nil {
		return err
	}
	stored, err := store.Get(rt.Profile)
	if err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			return exitf("no tokens stored for profile %s", rt.Profile)
		}
		return err
	}

	status := map[string]any{
		"profile":    rt.Profile,
		"expires_at": stored.ExpiresAt,
		"expired":    time.Now().After(stored.ExpiresAt),
	}
	if rt.JSONOutput {
		return writeJSONOutput(mustMarshal(status))
	}

	fmt.Fprintf(os.Stdout, "Profile %s expires %s (expired=%v)\n", rt.Profile, stored.ExpiresAt.Format(time.RFC3339), status["expired"])
	return nil
}

func authRefresh(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}

	cfg, _, err := loadConfig(rt)
	if err != nil {
		return err
	}
	profile := ensureProfile(cfg, rt.Profile, rt, config.Profile{})

	if err := require(profile.ClientID, "client-id"); err != nil {
		return err
	}
	if err := require(profile.ClientSecret, "client-secret"); err != nil {
		return err
	}

	client, store, err := newClient(context.Background(), rt, profile)
	if err != nil {
		return err
	}
	stored, err := store.Get(rt.Profile)
	if err != nil {
		return err
	}
	if stored.RefreshToken == "" {
		return exitf("refresh token missing for profile %s", rt.Profile)
	}

	refreshed, err := client.Refresh(context.Background(), stored.RefreshToken)
	if err != nil {
		return err
	}
	if refreshed.RefreshToken == "" {
		refreshed.RefreshToken = stored.RefreshToken
	}
	if err := store.Set(rt.Profile, refreshed); err != nil {
		return err
	}

	if rt.JSONOutput {
		return writeJSONOutput(mustMarshal(map[string]any{
			"status":     "ok",
			"expires_at": refreshed.ExpiresAt,
		}))
	}
	fmt.Fprintf(os.Stdout, "Refreshed token (expires %s)\n", refreshed.ExpiresAt.Format(time.RFC3339))
	return nil
}

func authLogout(c *cli.Context) error {
	rt, err := runtimeFrom(c)
	if err != nil {
		return err
	}
	store, err := storage.NewDefaultStore()
	if err != nil {
		return err
	}
	if err := store.Delete(rt.Profile); err != nil {
		return err
	}
	if rt.JSONOutput {
		return writeJSONOutput(mustMarshal(map[string]any{"status": "ok"}))
	}
	fmt.Fprintf(os.Stdout, "Deleted tokens for profile %s\n", rt.Profile)
	return nil
}

func waitForCallback(authURL, redirectURI, state string) (string, error) {
	callback, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	if callback.Host == "" {
		return "", errors.New("redirect uri must include host")
	}

	listener, err := net.Listen("tcp", callback.Host)
	if err != nil {
		return "", fmt.Errorf("listen on redirect host: %w", err)
	}
	defer listener.Close()

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)

	mux := http.NewServeMux()
	path := callback.Path
	if path == "" {
		path = "/"
	}
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if expected := state; expected != "" && q.Get("state") != expected {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("state mismatch"))
			errCh <- errors.New("state mismatch")
			return
		}
		code := q.Get("code")
		if code == "" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("missing code"))
			errCh <- errors.New("missing code")
			return
		}
		_, _ = w.Write([]byte("Authentication successful. You can close this window."))
		codeCh <- code
	})

	server := &http.Server{Handler: mux}
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	fmt.Fprintf(os.Stdout, "Open this URL in your browser:\n%s\n", authURL)

	select {
	case code := <-codeCh:
		_ = server.Shutdown(context.Background())
		return code, nil
	case err := <-errCh:
		_ = server.Shutdown(context.Background())
		return "", err
	case <-time.After(5 * time.Minute):
		_ = server.Shutdown(context.Background())
		return "", errors.New("timed out waiting for callback")
	}
}

func extractCode(input string) (string, error) {
	if input == "" {
		return "", errors.New("no input provided")
	}
	if strings.Contains(input, "code=") {
		parsed, err := url.Parse(input)
		if err == nil {
			code := parsed.Query().Get("code")
			if code != "" {
				return code, nil
			}
		}
	}
	return input, nil
}

func randomState() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return ""
	}
	return fmt.Sprintf("%x", data)
}

func mustMarshal(value any) []byte {
	data, _ := json.MarshalIndent(value, "", "  ")
	return data
}
