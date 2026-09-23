package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/BAITC-Hacks/hack-f917f57e-404-brain-not-found/internal/careerquest"
)

func main() {
	log.SetPrefix("career-quest: ")
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	cfg := careerquest.DefaultConfig()
	cfg.OpenAIAPIKey = os.Getenv("OPENAI_API_KEY")
	flags := flag.NewFlagSet("career-quest", flag.ContinueOnError)
	flags.SetOutput(output)
	port := flags.String("port", envOr("PORT", strconv.Itoa(cfg.Port)), "local HTTP port")
	flags.StringVar(&cfg.DataDir, "data-dir", envOr("CAREER_DATA_DIR", cfg.DataDir), "application data directory")
	flags.BoolVar(&cfg.SecureCookies, "secure-cookies", os.Getenv("CAREER_SECURE_COOKIES") == "1", "require HTTPS for session cookies")
	flags.DurationVar(&cfg.ShutdownTimeout, "shutdown-timeout", cfg.ShutdownTimeout, "time allowed for active requests during shutdown")
	flags.StringVar(&cfg.AIModel, "ai-model", envOr("OPENAI_MODEL", cfg.AIModel), "OpenAI model for development recommendations")
	flags.DurationVar(&cfg.AITimeout, "ai-timeout", cfg.AITimeout, "maximum time for AI generation and validation")
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: career-quest [options]")
		fmt.Fprintln(output, "       career-quest [options] provision <username> <employee-id|hr>")
		fmt.Fprintln(output, "\nOptions (place before the provision command):")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	parsedPort, err := strconv.Atoi(*port)
	if err != nil {
		return fmt.Errorf("invalid port %q: expected a number between 1 and 65535", *port)
	}
	cfg.Port = parsedPort

	switch remaining := flags.Args(); {
	case len(remaining) == 0:
		return careerquest.Run(ctx, cfg)
	case len(remaining) == 3 && remaining[0] == "provision":
		if err := careerquest.Provision(cfg, remaining[1], remaining[2]); err != nil {
			return err
		}
		log.Printf("Account provisioned. Deliver %s privately, then remove the delivery file. Restart the server to load account changes.", filepath.Join(cfg.DataDir, "private", "delivery-*.txt"))
		return nil
	default:
		flags.Usage()
		return fmt.Errorf("unknown command or invalid arguments")
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
