/*
Copyright © 2024 Daniel Hobe hobe@gmail.com

JWT token can be gotten from:
https://enlighten.enphaseenergy.com/entrez-auth-token?serial_num=YOURSERIAL_NUM_HERE
*/
package main

import (
	"context"
	"errors"
	_ "expvar"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	yaml "gopkg.in/yaml.v3"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	envoy "github.com/loafoe/go-envoy"
)

const (
	// MeasurementProduction is the measurement name for production data.
	MeasurementProduction = "production"
	// MeasurementTotalConsumption is the measurement name for total consumption data.
	MeasurementTotalConsumption = "total-consumption"
	// MeasurementNetConsumption is the measurement name for net consumption data.
	MeasurementNetConsumption = "net-consumption"
	// MeasurementInverter is the measurement name for inverter data.
	MeasurementInverter = "inverter"
	// MeasurementBattery is the measurement name for battery data.
	MeasurementBattery = "battery"

	// TagSource is the tag key for the data source.
	TagSource = "source"
	// TagMeasurementType is the tag key for the measurement type.
	TagMeasurementType = "measurement-type"
	// TagLineIdx is the tag key for the line index.
	TagLineIdx = "line-idx"
	// TagSerial is the tag key for the device serial number.
	TagSerial = "serial"

	// FieldP is the field key for real power (Watts).
	FieldP = "P"
	// FieldQ is the field key for reactive power (VAR).
	FieldQ = "Q"
	// FieldS is the field key for apparent power (VA).
	FieldS = "S"
	// FieldIrms is the field key for RMS current (Amps).
	FieldIrms = "I_rms"
	// FieldVrms is the field key for RMS voltage (Volts).
	FieldVrms = "V_rms"
	// FieldPercentFull is the field key for battery state of charge (percentage).
	FieldPercentFull = "percent-full"
	// FieldTemperature is the field key for battery temperature.
	FieldTemperature = "temperature"
)

var EnphaseBaseURL = "https://enlighten.enphaseenergy.com"

// ClientFactory is a function type that returns an EnvoyClient.
type ClientFactory func(cfg *Config) (EnvoyClient, error)

func defaultClientFactory(cfg *Config) (EnvoyClient, error) {
	return envoy.NewClient(cfg.Username,
		cfg.Password,
		cfg.SerialNumber,
		envoy.WithGatewayAddress(cfg.Address),
		envoy.WithDebug(true),
		envoy.WithJWT(cfg.JWT))
}

// Config holds the exporter configuration.
type Config struct {
	Username       string `yaml:"username"`
	Password       string `yaml:"password"`
	JWT            string `yaml:"jwt"`
	Address        string `yaml:"address"`
	SerialNumber   string `yaml:"serial"`
	SourceTag      string `yaml:"source"`
	InfluxDB       string `yaml:"influxdb"`
	InfluxDBToken  string `yaml:"influxdb_token"`
	InfluxDBOrg    string `yaml:"influxdb_org"`
	InfluxDBBucket string `yaml:"influxdb_bucket"`
	Interval       int    `yaml:"interval" validate:"required"`
	ExpVarPort     int    `yaml:"expvar_port"`
	RetryInterval  int    `yaml:"retry_interval"`
}

// Validate checks if the configuration is valid.
func (c *Config) Validate() error {
	if c.Address == "" {
		return fmt.Errorf("missing required configuration: address")
	}
	if c.SerialNumber == "" {
		return fmt.Errorf("missing required configuration: serial")
	}
	if (c.Username == "" && c.Password == "") && c.JWT == "" {
		return fmt.Errorf("missing Envoy authentication. Add username & password and optionally the JWT token")
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

// LoadConfig reads the configuration from the specified file.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open config file: %w", err)
	}
	defer f.Close()

	// Default interval
	cfg := Config{
		Interval: 5,
	}

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		return nil, fmt.Errorf("error reading config: %w", err)
	}
	return &cfg, nil
}

func lineToPoint(lineType string, line envoy.Line, idx int, sourceTag string) *influxdb2write.Point {
	return influxdb2.NewPointWithMeasurement(fmt.Sprintf("%s-line%d", lineType, idx)).
		AddTag(TagSource, sourceTag).
		AddTag(TagMeasurementType, lineType).
		AddTag(TagLineIdx, fmt.Sprintf("%d", idx)).
		AddField(FieldP, line.WNow).
		AddField(FieldQ, line.ReactPwr).
		AddField(FieldS, line.ApprntPwr).
		AddField(FieldIrms, line.RmsCurrent).
		AddField(FieldVrms, line.RmsVoltage).
		SetTime(time.Now())
}

func extractProductionStats(prod *envoy.ProductionResponse, sourceTag string) []*influxdb2write.Point {
	var ps []*influxdb2write.Point
	for _, measure := range prod.Production {
		if measure.MeasurementType == MeasurementProduction {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint(MeasurementProduction, line, i, sourceTag))
			}
		}
	}
	for _, measure := range prod.Consumption {
		if measure.MeasurementType == MeasurementTotalConsumption {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("consumption", line, i, sourceTag))
			}
		}
		if measure.MeasurementType == MeasurementNetConsumption {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("net", line, i, sourceTag))
			}
		}
	}
	return ps
}

func extractInverterStats(inverters *[]envoy.Inverter, sourceTag string) []*influxdb2write.Point {
	ps := make([]*influxdb2write.Point, len(*inverters))
	for i, inv := range *inverters {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("inverter-production-%s", inv.SerialNumber)).
			AddTag(TagSource, sourceTag).
			AddTag(TagMeasurementType, MeasurementInverter).
			AddTag(TagSerial, inv.SerialNumber).
			AddField(FieldP, inv.LastReportWatts).
			SetTime(time.Now())
		ps[i] = pt
	}
	return ps
}

func extractBatteryStats(batteries *[]envoy.Battery, sourceTag string) []*influxdb2write.Point {
	bats := make([]*influxdb2write.Point, len(*batteries))
	for i, inv := range *batteries {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("battery-%s", inv.SerialNum)).
			AddTag(TagSource, sourceTag).
			AddTag(TagMeasurementType, MeasurementBattery).
			AddTag(TagSerial, inv.SerialNum).
			AddField(FieldPercentFull, inv.PercentFull).
			AddField(FieldTemperature, inv.Temperature).
			SetTime(time.Now())
		bats[i] = pt
	}
	return bats
}

// EnvoyClient is an interface that defines the methods for interacting with the Envoy.
type EnvoyClient interface {
	Production() (*envoy.ProductionResponse, error)
	Inverters() (*[]envoy.Inverter, error)
	Batteries() (*[]envoy.Battery, error)
	InvalidateSession()
}

// PointWriter abstracts the InfluxDB WriteAPIBlocking.
type PointWriter interface {
	WritePoint(ctx context.Context, point ...*influxdb2write.Point) error
}

func scrape(ctx context.Context, e EnvoyClient, writeAPI PointWriter, sourceTag string) int {
	prod, err := e.Production()
	if err != nil {
		slog.Error("Error getting Production data from Envoy", "error", err, "operation", "e.Production")
	}
	var points []*influxdb2write.Point
	if prod != nil && len(prod.Production) > 0 {
		points = append(points, extractProductionStats(prod, sourceTag)...)
	}
	inverters, err := e.Inverters()
	if err != nil {
		slog.Error("Error getting Inverter data from Envoy", "error", err, "operation", "e.Inverters")
	}
	if inverters != nil && len(*inverters) > 0 {
		points = append(points, extractInverterStats(inverters, sourceTag)...)
	}

	batteries, err := e.Batteries()
	if err != nil {
		slog.Error("Error getting Battery data from Envoy", "error", err, "operation", "e.Batteries")
	} else if batteries != nil {
		points = append(points, extractBatteryStats(batteries, sourceTag)...)
	}

	if len(points) > 0 {
		err = writeAPI.WritePoint(ctx, points...)
		if err != nil {
			slog.Error("Error writing data to InfluxDB",
				"error", err,
				"points_count", len(points),
				"target", "influxdb")
		}
	}
	return len(points)
}

// AuthenticateWithEnphase gets a new JWT token from Enphase
func AuthenticateWithEnphase(username, password, serial string) (string, error) {
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar: jar,
	}

	// Login
	resp, err := client.PostForm(EnphaseBaseURL+"/login/login", url.Values{
		"user[email]":    {username},
		"user[password]": {password},
	})
	if err != nil {
		return "", fmt.Errorf("login failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("login failed with status: %s", resp.Status)
	}

	// Get Token
	tokenURL := fmt.Sprintf("%s/entrez-auth-token?serial_num=%s", EnphaseBaseURL, serial)
	resp, err = client.Get(tokenURL)
	if err != nil {
		return "", fmt.Errorf("failed to get token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("get token failed with status: %s", resp.Status)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	return string(body), nil
}

func scrapeLoop(ctx context.Context, cfg *Config, writeAPI PointWriter, clientFactory ClientFactory) {
	slog.Info("Connecting to envoy", "address", cfg.Address)
	var e EnvoyClient
	var err error

	// Initial connection loop
	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	retryInterval := time.Duration(cfg.RetryInterval) * time.Second
	if retryInterval == 0 {
		retryInterval = 5 * time.Second
	}

	// Retry logic for initial connection
	for {
		select {
		case <-ctx.Done():
			return
		default:
			e, err = clientFactory(cfg)
			if err != nil {
				slog.Error("Error connecting to Envoy", "error", err)
				slog.Info("Retrying connection...", "wait", retryInterval)
				select {
				case <-ctx.Done():
					return
				case <-time.After(retryInterval):
					continue
				}
			}
		}
		break // Connected
	}

	// Main scrape loop
	// Perform an immediate scrape first
	scrape(ctx, e, writeAPI, cfg.SourceTag)

	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping scrape loop...")
			return
		case <-ticker.C:
			tStat := time.Now()
			numPoints := scrape(ctx, e, writeAPI, cfg.SourceTag)
			scrapeDuration := time.Since(tStat)
			slog.Info("Scrape finished",
				"duration", scrapeDuration,
				"points", numPoints)
		}
	}
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("envoy-exporter", flag.ContinueOnError)
	var cfgFile string
	var debug bool
	fs.StringVar(&cfgFile, "config", "envoy.yaml", "Path to config file.")
	fs.BoolVar(&debug, "debug", false, "Enable debug logging.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Setup structured logger
	logLevel := slog.LevelInfo
	if debug {
		logLevel = slog.LevelDebug
	}
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: logLevel,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		slog.Info("Received shutdown signal")
		cancel()
	}()

	slog.Info("Reading Config", "file", cfgFile)
	cfg, err := LoadConfig(cfgFile)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	if cfg.JWT == "" && cfg.Username != "" && cfg.Password != "" {
		slog.Info("JWT token missing, attempting to fetch from Enphase...")
		token, err := AuthenticateWithEnphase(cfg.Username, cfg.Password, cfg.SerialNumber)
		if err != nil {
			slog.Error("Failed to fetch JWT token", "error", err)
		} else {
			slog.Info("Successfully fetched JWT token")
			cfg.JWT = token
		}
	}

	// Start expvar server with graceful shutdown
	go func() {
		port := cfg.ExpVarPort
		if port == 0 {
			port = 6666
		}
		addr := fmt.Sprintf("localhost:%d", port)
		srv := &http.Server{
			Addr:    addr,
			Handler: nil, // Use DefaultServeMux for expvar
		}

		go func() {
			slog.Info("Starting expvar server", "port", port)
			if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				slog.Error("expvar server failed", "error", err)
			}
		}()

		<-ctx.Done()
		slog.Info("Shutting down expvar server...")
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Error("expvar server shutdown failed", "error", err)
		}
	}()

	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("configuration validation failed: %w", err)
	}

	slog.Info("Starting Envoy Exporter", "go_version", runtime.Version())
	slog.Debug("Scraping envoy",
		"address", cfg.Address,
		"serial", cfg.SerialNumber,
		"interval", cfg.Interval)
	slog.Debug("Writing to Influxdb",
		"url", cfg.InfluxDB,
		"bucket", cfg.InfluxDBBucket)

	// Initialize InfluxDB Client
	client := influxdb2.NewClient(cfg.InfluxDB, cfg.InfluxDBToken)
	defer client.Close()
	writeAPI := client.WriteAPIBlocking(cfg.InfluxDBOrg, cfg.InfluxDBBucket)

	scrapeLoop(ctx, cfg, writeAPI, defaultClientFactory)
	return nil
}
