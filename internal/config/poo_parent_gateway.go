package config

import (
	"strings"
	"time"
)

const (
	DefaultPoOParentGatewayURL          = "http://127.0.0.1:15005/v1/proof/relay"
	DefaultPoOParentGatewayTimeout      = 10 * time.Minute
	DefaultPoOParentGatewayMaxBodyBytes = int64(64 * 1024 * 1024)
)

// PoOParentGatewayConfig controls the proof-of-observation Nitro Enclave relay.
type PoOParentGatewayConfig struct {
	Enabled      bool   `yaml:"enabled" json:"enabled"`
	Required     *bool  `yaml:"required,omitempty" json:"required,omitempty"`
	URL          string `yaml:"url" json:"url"`
	AuthMode     string `yaml:"auth-mode" json:"auth-mode"`
	Timeout      string `yaml:"timeout" json:"timeout"`
	MaxBodyBytes int64  `yaml:"max-body-bytes" json:"max-body-bytes"`
	CAFile       string `yaml:"ca-file,omitempty" json:"ca-file,omitempty"`
	CertFile     string `yaml:"cert-file,omitempty" json:"cert-file,omitempty"`
	KeyFile      string `yaml:"key-file,omitempty" json:"key-file,omitempty"`
	ServerName   string `yaml:"server-name,omitempty" json:"server-name,omitempty"`
}

func (c PoOParentGatewayConfig) IsRequired() bool {
	return c.Required == nil || *c.Required
}

func (c PoOParentGatewayConfig) RelayURL() string {
	if value := strings.TrimSpace(c.URL); value != "" {
		return value
	}
	return DefaultPoOParentGatewayURL
}

func (c PoOParentGatewayConfig) RequestTimeout() time.Duration {
	value := strings.TrimSpace(c.Timeout)
	if value == "" {
		return DefaultPoOParentGatewayTimeout
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return DefaultPoOParentGatewayTimeout
	}
	return parsed
}

func (c PoOParentGatewayConfig) BodyLimit() int64 {
	if c.MaxBodyBytes <= 0 {
		return DefaultPoOParentGatewayMaxBodyBytes
	}
	return c.MaxBodyBytes
}
