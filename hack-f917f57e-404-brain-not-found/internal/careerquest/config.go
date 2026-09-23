package careerquest

import (
	"fmt"
	"strings"
	"time"
)

// Config contains runtime settings. The command is responsible for reading
// environment variables and flags; application code receives explicit values.
type Config struct {
	Port            int
	DataDir         string
	SecureCookies   bool
	ShutdownTimeout time.Duration
	OpenAIAPIKey    string `json:"-"`
	AIModel         string
	AITimeout       time.Duration
}

// DefaultConfig returns the local application's default settings.
func DefaultConfig() Config {
	return Config{
		Port:            567,
		DataDir:         "data",
		ShutdownTimeout: 15 * time.Second,
		AIModel:         defaultAIModel,
		AITimeout:       20 * time.Second,
	}
}

func (c Config) validate() error {
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if strings.TrimSpace(c.DataDir) == "" {
		return fmt.Errorf("data directory must not be empty")
	}
	if c.ShutdownTimeout <= 0 {
		return fmt.Errorf("shutdown timeout must be greater than zero")
	}
	if c.AITimeout < time.Second || c.AITimeout > 60*time.Second {
		return fmt.Errorf("AI timeout must be between 1s and 60s")
	}
	if strings.TrimSpace(c.AIModel) == "" {
		return fmt.Errorf("AI model must not be empty")
	}
	return nil
}
