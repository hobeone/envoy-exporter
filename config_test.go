package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	t.Parallel()
	content := []byte(`
address: https://192.168.1.100
serial: 123456
username: user@example.com
password: secret
influxdb: http://localhost:8086
influxdb_token: mytoken
influxdb_org: myorg
influxdb_bucket: mybucket
interval: 10
`)
	f, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cfg, err := LoadConfig(f.Name())
	require.NoError(t, err)
	assert.Equal(t, "https://192.168.1.100", cfg.Address)
	assert.Equal(t, "123456", cfg.SerialNumber)
	assert.Equal(t, "user@example.com", cfg.Username)
	assert.Equal(t, 10, cfg.Interval)
}

func TestLoadConfig_Defaults(t *testing.T) {
	t.Parallel()
	content := []byte(`
address: https://192.168.1.100
serial: 123456
jwt: sometoken
influxdb: http://localhost:8086
influxdb_token: tok
influxdb_org: org
influxdb_bucket: bucket
`)
	f, err := os.CreateTemp("", "config-*.yaml")
	require.NoError(t, err)
	defer os.Remove(f.Name())

	_, err = f.Write(content)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	cfg, err := LoadConfig(f.Name())
	require.NoError(t, err)
	assert.Equal(t, 30, cfg.Interval, "default interval")
	assert.Equal(t, 5, cfg.RetryInterval, "default retry interval")
	assert.Equal(t, "info", cfg.LogLevel, "default log level")
}

func TestLoadConfig_MissingFile(t *testing.T) {
	t.Parallel()
	_, err := LoadConfig("/nonexistent/path.yaml")
	assert.Error(t, err)
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()
	base := Config{
		Address:        "https://192.168.1.100",
		SerialNumber:   "12345",
		Username:       "user",
		Password:       "pass",
		InfluxDB:       "http://influx:8086",
		InfluxDBBucket: "bucket",
		InfluxDBToken:  "token",
		InfluxDBOrg:    "org",
	}

	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:    "valid with username+password",
			mutate:  nil,
			wantErr: false,
		},
		{
			name: "valid with JWT only",
			mutate: func(c *Config) {
				c.Username = ""
				c.Password = ""
				c.JWT = "sometoken"
			},
			wantErr: false,
		},
		{
			name: "valid with all auth fields",
			mutate: func(c *Config) {
				c.JWT = "sometoken"
			},
			wantErr: false,
		},
		{
			name: "missing address",
			mutate: func(c *Config) {
				c.Address = ""
			},
			wantErr: true,
		},
		{
			name: "missing serial",
			mutate: func(c *Config) {
				c.SerialNumber = ""
			},
			wantErr: true,
		},
		{
			name: "missing all auth",
			mutate: func(c *Config) {
				c.Username = ""
				c.Password = ""
				c.JWT = ""
			},
			wantErr: true,
		},
		{
			name: "missing influxdb url",
			mutate: func(c *Config) {
				c.InfluxDB = ""
			},
			wantErr: true,
		},
		{
			name: "missing influxdb bucket",
			mutate: func(c *Config) {
				c.InfluxDBBucket = ""
			},
			wantErr: true,
		},
		{
			name: "missing influxdb token",
			mutate: func(c *Config) {
				c.InfluxDBToken = ""
			},
			wantErr: true,
		},
		{
			name: "missing influxdb org",
			mutate: func(c *Config) {
				c.InfluxDBOrg = ""
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := base // copy
			if tt.mutate != nil {
				tt.mutate(&cfg)
			}
			err := cfg.Validate()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
