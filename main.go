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
	"net/http"
	"os"
	"runtime"
	"time"

	log "github.com/sirupsen/logrus"
	yaml "gopkg.in/yaml.v3"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	envoy "github.com/loafoe/go-envoy"
)

var (
	cfgFile string
	cfg     Config
)

func loadAndValidateConfig(filePath string) (Config, error) {
	var cfg Config

	f, err := os.Open(filePath)
	if err != nil {
		return cfg, fmt.Errorf("opening config file %s: %w", filePath, err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		return cfg, fmt.Errorf("decoding config file %s: %w", filePath, err)
	}

	// Apply default interval if not set in config or set to 0
	if cfg.Interval == 0 {
		// log.Info("Interval not specified in config or set to 0, using default value of 5 seconds.") // Logging handled in main
		cfg.Interval = 5
	}

	if cfg.Address == "" {
		return cfg, fmt.Errorf("Missing required configuration: address")
	}
	if cfg.SerialNumber == "" {
		return cfg, fmt.Errorf("Missing required configuration: serial")
	}
	if (cfg.Username == "" && cfg.Password == "") && cfg.JWT == "" {
		return cfg, fmt.Errorf("Missing Envoy authentication. Add username & password and optionally the JWT token")
	}
	if cfg.InfluxDB == "" {
		return cfg, fmt.Errorf("Missing required configuration: influxdb")
	}
	if cfg.InfluxDBBucket == "" {
		return cfg, fmt.Errorf("Missing required configuration: influxdb_bucket")
	}
	if cfg.InfluxDBToken == "" {
		return cfg, fmt.Errorf("Missing required configuration: influxdb_token")
	}
	if cfg.InfluxDBOrg == "" {
		return cfg, fmt.Errorf("Missing required configuration: influxdb_org")
	}

	return cfg, nil
}

type EnvoyClientInterface interface {
	CommCheck() (*[]envoy.CommCheckDevice, error)
	Production() (*envoy.ProductionResponse, error)
	Inverters() (*[]envoy.Inverter, error)
	Batteries() (*[]envoy.Battery, error)
	InvalidateSession()
}

func lineToPoint(lineType string, line envoy.Line, idx int, ts time.Time) *influxdb2write.Point {
	return influxdb2.NewPointWithMeasurement(fmt.Sprintf("%s-line%d", lineType, idx)).
		AddTag("source", cfg.SourceTag).
		AddTag("measurement-type", lineType).
		AddTag("line-idx", fmt.Sprintf("%d", idx)).
		AddField("P", line.WNow).
		AddField("Q", line.ReactPwr).
		AddField("S", line.ApprntPwr).
		AddField("I_rms", line.RmsCurrent).
		AddField("V_rms", line.RmsVoltage).
		SetTime(ts)
}

func extractProductionStats(prod *envoy.ProductionResponse, ts time.Time) []*influxdb2write.Point {
	var ps []*influxdb2write.Point
	for _, measure := range prod.Production {
		if measure.MeasurementType == "production" {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("production", line, i, ts))
			}
		}
	}
	for _, measure := range prod.Consumption {
		if measure.MeasurementType == "total-consumption" {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("consumption", line, i, ts))
			}
		}
		if measure.MeasurementType == "net-consumption" {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("net", line, i, ts))
			}
		}
	}
	return ps
}

func extractInverterStats(inverters *[]envoy.Inverter, ts time.Time) []*influxdb2write.Point {
	ps := make([]*influxdb2write.Point, len(*inverters))
	for i, inv := range *inverters {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("inverter-production-%s", inv.SerialNumber)).
			AddTag("source", cfg.SourceTag).
			AddTag("measurement-type", "inverter").
			AddTag("serial", inv.SerialNumber).
			AddField("P", inv.LastReportWatts).
			SetTime(ts)
		ps[i] = pt
	}

	return ps
}

func extractBatteryStats(batteries *[]envoy.Battery, ts time.Time) []*influxdb2write.Point {
	bats := make([]*influxdb2write.Point, len(*batteries))
	for i, inv := range *batteries {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("battery-%s", inv.SerialNum)).
			AddTag("source", cfg.SourceTag).
			AddTag("measurement-type", "battery").
			AddTag("serial", inv.SerialNum).
			AddField("percent-full", inv.PercentFull).
			AddField("temperature", inv.Temperature).
			SetTime(ts)
		bats[i] = pt
	}
	return bats
}

// scrape function now takes EnvoyClientInterface
func scrape(e EnvoyClientInterface, influxWriter influxdb2write.WriteAPI, scrapeTime time.Time) (numPoints int, clientInvalidated bool) {
	// scrapeTime is now passed as an argument
	clientInvalidated = false // Ensure initialized
	cr, err := e.CommCheck()
	if err != nil {
		log.Errorf("Error during communication check with Envoy: %v", err)
		e.InvalidateSession() // Token expired?
		clientInvalidated = true
		return 0, clientInvalidated
	}
	if cr != nil {
		log.Infof("Found devices: %d", len(*cr))
	}

	var points []*influxdb2write.Point

	prod, err := e.Production()
	if err != nil {
		log.Errorf("Error getting Production data from Envoy: %v", err)
		// We don't invalidate the client here as it might be a temporary issue
	}
	if prod != nil && len(prod.Production) > 0 {
		points = append(points, extractProductionStats(prod, scrapeTime)...)
	}

	inverters, err := e.Inverters()
	if err != nil {
		log.Errorf("Error getting Inverter data from Envoy: %v", err)
		// We don't invalidate the client here
	}
	if inverters != nil && len(*inverters) > 0 {
		points = append(points, extractInverterStats(inverters, scrapeTime)...)
	}

	batteries, err := e.Batteries()
	if err != nil {
		log.Errorf("Error getting Battery data from Envoy: %v", err)
		// We don't invalidate the client here
	} else if batteries != nil && len(*batteries) > 0 { // Ensure batteries is not nil before appending
		points = append(points, extractBatteryStats(batteries, scrapeTime)...)
	}

	if len(points) == 0 {
		log.Info("No data points collected, skipping InfluxDB write.")
		return 0, clientInvalidated
	}

	// client := influxdb2.NewClient(cfg.InfluxDB, cfg.InfluxDBToken) // Removed
	// writeAPI := client.WriteAPIBlocking(cfg.InfluxDBOrg, cfg.InfluxDBBucket) // Removed
	err = influxWriter.WritePoint(context.Background(), points...)
	if err != nil {
		log.Errorf("Error writing data to InfluxDB: %v", err)
		// If InfluxDB write fails, we don't necessarily need to invalidate the Envoy client
	}
	return len(points), clientInvalidated
}

func scrapeLoop(influxWriter influxdb2write.WriteAPI) {
	log.Infof("Connecting to envoy at: %s", cfg.Address)
	connected := false
	var err error
	var e *envoy.Client // e is the concrete type here
	interval := time.Duration(cfg.Interval) * time.Second

	for { // Outer loop for reconnection
		if !connected {
			log.Info("Attempting to connect to Envoy...")
			// e is assigned the concrete *envoy.Client
			e, err = envoy.NewClient(cfg.Username,
				cfg.Password,
				cfg.SerialNumber,
				envoy.WithGatewayAddress(cfg.Address),
				envoy.WithDebug(true), // Consider making this configurable
				envoy.WithJWT(cfg.JWT))
			if err != nil {
				log.Errorf("Error connecting to Envoy: %v. Retrying in %v...", err, interval)
				time.Sleep(interval)
				continue // Retry connection
			}
			log.Info("Successfully connected to Envoy.")
			connected = true
		}

		tStat := time.Now() // This tStat is for measuring scrape duration
		// The actual scrapeTime (timestamp for data points) is passed to scrape
		numPoints, clientInvalidated := scrape(e, influxWriter, tStat) // Pass concrete e, which implements EnvoyClientInterface
		scrapeDuration := time.Since(tStat)

		if clientInvalidated {
			log.Info("Client session invalidated or communication error, attempting to reconnect to Envoy...")
			connected = false // Trigger reconnection
			// Potentially add a small delay before retrying connection immediately
			time.Sleep(time.Second * 1) // Brief pause before trying to reconnect
			continue                  // Jump to the start of the loop to reconnect
		}

		timeToSleep := time.Until(tStat.Add(interval))
		log.Infof("Scrape took: %v, found %d points, sleeping %v", scrapeDuration, numPoints, timeToSleep)
		if timeToSleep > 0 {
			time.Sleep(timeToSleep)
		} else {
			// If scrape took longer than interval, log it and proceed immediately to next scrape
			log.Warnf("Scrape duration (%v) exceeded interval (%v). Starting next scrape immediately.", scrapeDuration, interval)
		}
	}
}

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
}

func main() {
	go func() {
		// For expvar exporting to netdata
		log.Println(http.ListenAndServe("localhost:6666", nil))
	}()

	flag.StringVar(&cfgFile, "config", "envoy.yaml", "Path to config file.")
	flag.Parse()

	log.Infof("Reading Config: %s", cfgFile)
	var err error
	cfg, err = loadAndValidateConfig(cfgFile)
	if err != nil {
		log.Fatalf("Error loading configuration: %v", err)
	}

	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	// Logging for interval default can be done here if needed,
	// for example by comparing the loaded cfg.Interval with the default value
	// and checking if the original file had it as 0 or missing.
	// For simplicity, we'll rely on the fact that loadAndValidateConfig sets it.

	log.Infof("Build with Go version: %s\n", runtime.Version())
	log.Infof("Scraping envoy at: %s with serial number %s every %d seconds", cfg.Address, cfg.SerialNumber, cfg.Interval)
	log.Infof("Writing to Influxdb: %s, Bucket '%s'", cfg.InfluxDB, cfg.InfluxDBBucket)

	influxClient := influxdb2.NewClient(cfg.InfluxDB, cfg.InfluxDBToken)
	// Ensure the client is properly closed when main exits
	defer influxClient.Close()

	// You can perform a health check if desired, though it might be overly complex for this step
	// _, err := influxClient.Health(context.Background())
	// if err != nil {
	//     log.Fatalf("Error connecting to InfluxDB for health check: %v", err)
	// }
	// log.Info("Successfully connected to InfluxDB.")

	influxWriter := influxClient.WriteAPIBlocking(cfg.InfluxDBOrg, cfg.InfluxDBBucket)

	scrapeLoop(influxWriter)
}
