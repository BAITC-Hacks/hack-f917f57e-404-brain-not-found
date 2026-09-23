package careerquest

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"time"
)

func openApplication(cfg Config) (*App, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	store, err := openStore(filepath.Join(cfg.DataDir, "state.json"))
	if err != nil {
		return nil, fmt.Errorf("load application data: %w", err)
	}
	app, err := newApp(store)
	if err != nil {
		return nil, fmt.Errorf("load page templates: %w", err)
	}
	app.accounts, err = openAccountStore(filepath.Join(cfg.DataDir, "private", "accounts.json"))
	if err != nil {
		return nil, fmt.Errorf("load accounts: %w", err)
	}
	app.secureCookies = cfg.SecureCookies
	app.providerService.Budget = cfg.AITimeout
	app.providerService.GenerationBudget = cfg.AITimeout * 4 / 5
	if cfg.OpenAIAPIKey != "" {
		app.provider = newOpenAIProvider(cfg.OpenAIAPIKey, cfg.AIModel)
	}
	return app, nil
}

// Run starts the local HTTP server and waits until it fails or ctx is canceled.
// On cancellation, active requests have ShutdownTimeout to finish before close.
func Run(ctx context.Context, cfg Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	app, err := openApplication(cfg)
	if err != nil {
		return err
	}
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port))
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", address, err)
	}
	defer listener.Close()
	if err := app.initializeAccounts(filepath.Join(cfg.DataDir, "private")); err != nil {
		return fmt.Errorf("initialize accounts: %w", err)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           app.routes(),
		ReadHeaderTimeout: 3 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      cfg.AITimeout + 5*time.Second,
		IdleTimeout:       60 * time.Second,
	}
	serveErrors := make(chan error, 1)
	go func() { serveErrors <- server.Serve(listener) }()
	log.Printf("Career Quest running at http://%s", address)

	select {
	case err := <-serveErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve HTTP: %w", err)
	case <-ctx.Done():
		log.Print("Shutting down Career Quest")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
			return fmt.Errorf("shut down HTTP server: %w", err)
		}
		if err := <-serveErrors; err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}

// Provision creates or resets an account, writing its credentials to a private
// delivery file. The server must be stopped while this operation runs.
func Provision(cfg Config, username, target string) error {
	app, err := openApplication(cfg)
	if err != nil {
		return err
	}
	if err := app.provisionCLI(username, target, filepath.Join(cfg.DataDir, "private")); err != nil {
		return fmt.Errorf("provision account: %w", err)
	}
	return nil
}
