package awscollector

import (
	"errors"
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type ServiceConfig struct {
	Version    int                 `yaml:"version"`
	Provider   string              `yaml:"provider"`
	Collection Collection          `yaml:"collection"`
	Services   map[string][]string `yaml:"services"`
	Paths      map[string]string   `yaml:"paths"`
}

type Collection struct {
	Regions               []string         `yaml:"regions"`
	Accounts              []string         `yaml:"accounts"`
	PollInterval          time.Duration    `yaml:"poll_interval"`
	MaxConcurrentAPICalls int              `yaml:"max_concurrent_api_calls"`
	AssumeRole            AssumeRoleConfig `yaml:"assume_role"`
}

type AssumeRoleConfig struct {
	Enabled    bool   `yaml:"enabled"`
	RoleARN    string `yaml:"role_arn"`
	ExternalID string `yaml:"external_id"`
}

func LoadServiceConfig(path string) (ServiceConfig, error) {
	if path == "" {
		return ServiceConfig{}, errors.New("AWS service config path is required")
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ServiceConfig{}, fmt.Errorf("read AWS service config: %w", err)
	}
	var config ServiceConfig
	if err := yaml.Unmarshal(body, &config); err != nil {
		return ServiceConfig{}, fmt.Errorf("parse AWS service config: %w", err)
	}
	if config.Provider != "aws" {
		return ServiceConfig{}, errors.New("AWS service config provider must be aws")
	}
	if len(config.Collection.Regions) == 0 {
		config.Collection.Regions = []string{"us-east-1"}
	}
	if config.Collection.PollInterval <= 0 {
		config.Collection.PollInterval = 60 * time.Second
	}
	if config.Collection.MaxConcurrentAPICalls <= 0 {
		config.Collection.MaxConcurrentAPICalls = 20
	}
	return config, nil
}
