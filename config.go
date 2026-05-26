package main

import (
	"fmt"
	"os"
	"sync"

	yaml "gopkg.in/yaml.v3"
)

// Config holds all configuration for the exporter.
type Config struct {
	mu *sync.RWMutex

	// Envoy gateway
	Address      string `yaml:"address"`
	SerialNumber string `yaml:"serial"`

	// Authentication: provide username+password (JWT auto-fetched), jwt, or both.
	Username string `yaml:"username"`
	Password string `yaml:"password"`
	JWT      string `yaml:"jwt"`

	// InfluxDB v2
	InfluxDB       string `yaml:"influxdb"`
	InfluxDBToken  string `yaml:"influxdb_token"`
	InfluxDBOrg    string `yaml:"influxdb_org"`
	InfluxDBBucket string `yaml:"influxdb_bucket"`

	// Optional
	SourceTag          string `yaml:"source"`
	Interval           int    `yaml:"interval"`
	RetryInterval      int    `yaml:"retry_interval"`
	JWTRefreshLeadTime int    `yaml:"jwt_refresh_lead_time"` // minutes before expiry to refresh; default 60
	PersistJWT         bool   `yaml:"persist_jwt"`           // write refreshed JWT back to the config file
	LogLevel           string `yaml:"log_level"`             // debug, info, warn, error; default info
	ExpvarPort         int    `yaml:"expvar_port"`           // port for expvar HTTP server; default 6666
	InsecureSkipVerify bool   `yaml:"tls_insecure_skip_verify"` // skip gateway TLS verification; default false
}

// GetJWT returns the JWT in a thread-safe manner.
func (c *Config) GetJWT() string {
	if c.mu == nil {
		return c.JWT
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.JWT
}

// SetJWT updates the JWT in a thread-safe manner.
func (c *Config) SetJWT(jwt string) {
	if c.mu == nil {
		c.mu = &sync.RWMutex{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.JWT = jwt
}

// Validate returns an error if the configuration is missing required fields.
func (c *Config) Validate() error {
	if c.Address == "" {
		return fmt.Errorf("missing required configuration: address")
	}
	if c.SerialNumber == "" {
		return fmt.Errorf("missing required configuration: serial")
	}
	if c.Username == "" && c.Password == "" && c.JWT == "" {
		return fmt.Errorf("missing Envoy authentication: provide username+password or jwt")
	}
	if c.InfluxDB == "" {
		return fmt.Errorf("missing required configuration: influxdb")
	}
	if c.InfluxDBBucket == "" {
		return fmt.Errorf("missing required configuration: influxdb_bucket")
	}
	if c.InfluxDBToken == "" {
		return fmt.Errorf("missing required configuration: influxdb_token")
	}
	if c.InfluxDBOrg == "" {
		return fmt.Errorf("missing required configuration: influxdb_org")
	}
	return nil
}

// LoadConfig reads and decodes a YAML config file from path.
// Optional fields default to sensible values if absent.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer func() { _ = f.Close() }()

	cfg := Config{
		mu:            &sync.RWMutex{},
		Interval:      30,
		RetryInterval: 5,
		LogLevel:      "info",
		ExpvarPort:    6666,
	}

	if err := yaml.NewDecoder(f).Decode(&cfg); err != nil {
		return nil, fmt.Errorf("error reading config: %w", err)
	}
	return &cfg, nil
}
