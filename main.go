/*
Copyright © 2024 Daniel Hobe hobe@gmail.com

JWT token can be gotten from:
https://enlighten.enphaseenergy.com/entrez-auth-token?serial_num=YOURSERIAL_NUM_HERE
*/
package main

import (
	"context"
	_ "expvar"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
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
	MeasurementProduction       = "production"
	MeasurementTotalConsumption = "total-consumption"
	MeasurementNetConsumption   = "net-consumption"
	MeasurementInverter         = "inverter"
	MeasurementBattery          = "battery"

	TagSource          = "source"
	TagMeasurementType = "measurement-type"
	TagLineIdx         = "line-idx"
	TagSerial          = "serial"

	FieldP           = "P"
	FieldQ           = "Q"
	FieldS           = "S"
	FieldIrms        = "I_rms"
	FieldVrms        = "V_rms"
	FieldPercentFull = "percent-full"
	FieldTemperature = "temperature"
)

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
}

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

type EnvoyClient interface {
	Production() (*envoy.ProductionResponse, error)
	Inverters() (*[]envoy.Inverter, error)
	Batteries() (*[]envoy.Battery, error)
	InvalidateSession()
}

// PointWriter abstracts the InfluxDB WriteAPIBlocking
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

func scrapeLoop(ctx context.Context, cfg *Config, writeAPI PointWriter) {
	slog.Info("Connecting to envoy", "address", cfg.Address)
	var e EnvoyClient
	var err error
	
	// Initial connection loop
	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	// Retry logic for initial connection
	for {
		select {
		case <-ctx.Done():
			return
		default:
			e, err = envoy.NewClient(cfg.Username,
				cfg.Password,
				cfg.SerialNumber,
				envoy.WithGatewayAddress(cfg.Address),
				envoy.WithDebug(true),
				envoy.WithJWT(cfg.JWT))
			if err != nil {
				slog.Error("Error connecting to Envoy", "error", err)
				slog.Info("Retrying connection in 5 seconds...")
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
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
	// Setup structured logger (JSON handler or Text handler)
	// Using TextHandler for now to mimic previous output but structured
	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	logger := slog.New(handler)
	slog.SetDefault(logger)

	// Setup context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		slog.Info("Received shutdown signal")
		cancel()
	}()

	var cfgFile string
	flag.StringVar(&cfgFile, "config", "envoy.yaml", "Path to config file.")
	flag.Parse()

	// Default interval
	cfg := Config{
		Interval: 5,
	}

	slog.Info("Reading Config", "file", cfgFile)
	f, err := os.Open(cfgFile)
	if err != nil {
		slog.Error("Failed to open config file", "error", err)
		os.Exit(1)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		slog.Error("Error reading config", "error", err)
		os.Exit(1)
	}

	go func() {
		// For expvar exporting to netdata
		port := cfg.ExpVarPort
		if port == 0 {
			port = 6666
		}
		slog.Info("Starting expvar server", "port", port)
		slog.Error("expvar server failed", "error", http.ListenAndServe(fmt.Sprintf("localhost:%d", port), nil))
	}()

	if err := cfg.Validate(); err != nil {
		slog.Error("Configuration validation failed", "error", err)
		os.Exit(1)
	}

	slog.Info("Starting Envoy Exporter", "go_version", runtime.Version())
	// Debug logs - slog defaults to Info, so these won't show unless level is changed above
	// But we'll keep them as Debug
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

	scrapeLoop(ctx, &cfg, writeAPI)
}