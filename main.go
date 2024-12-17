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

func lineToPoint(lineType string, line envoy.Line, idx int) *influxdb2write.Point {
	return influxdb2.NewPointWithMeasurement(fmt.Sprintf("%s-line%d", lineType, idx)).
		AddTag("source", cfg.SourceTag).
		AddTag("measurement-type", lineType).
		AddTag("line-idx", fmt.Sprintf("%d", idx)).
		AddField("P", line.WNow).
		AddField("Q", line.ReactPwr).
		AddField("S", line.ApprntPwr).
		AddField("I_rms", line.RmsCurrent).
		AddField("V_rms", line.RmsVoltage).
		SetTime(time.Now())
}

func extractProductionStats(prod *envoy.ProductionResponse) []*influxdb2write.Point {
	var ps []*influxdb2write.Point
	for _, measure := range prod.Production {
		if measure.MeasurementType == "production" {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("production", line, i))
			}
		}
	}
	for _, measure := range prod.Consumption {
		if measure.MeasurementType == "total-consumption" {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("consumption", line, i))
			}
		}
		if measure.MeasurementType == "net-consumption" {
			for i, line := range measure.Lines {
				ps = append(ps, lineToPoint("net", line, i))
			}
		}
	}
	return ps
}

func extractInverterStats(inverters *[]envoy.Inverter) []*influxdb2write.Point {
	ps := make([]*influxdb2write.Point, len(*inverters))
	for i, inv := range *inverters {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("inverter-production-%s", inv.SerialNumber)).
			AddTag("source", cfg.SourceTag).
			AddTag("measurement-type", "inverter").
			AddTag("serial", inv.SerialNumber).
			AddField("P", inv.LastReportWatts).
			SetTime(time.Now())
		ps[i] = pt
	}

	return ps
}

func extractBatteryStats(batteries *[]envoy.Battery) []*influxdb2write.Point {
	bats := make([]*influxdb2write.Point, len(*batteries))
	for i, inv := range *batteries {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("battery-%s", inv.SerialNum)).
			AddTag("source", cfg.SourceTag).
			AddTag("measurement-type", "battery").
			AddTag("serial", inv.SerialNum).
			AddField("percent-full", inv.PercentFull).
			AddField("temperature", inv.Temperature).
			SetTime(time.Now())
		bats[i] = pt
	}
	return bats
}

func scrape(e *envoy.Client) int {
	cr, resp, err := e.CommCheck()
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			e.InvalidateSession() // Token expired?
		}
	}
	if cr != nil {
		log.Infof("Found devices: %d", len(*cr))
	}
	prod, _, err := e.Production()
	if err != nil {
		log.Errorf("Error getting Production data from Envoy: %v", err)
	}
	var points []*influxdb2write.Point
	if prod != nil && len(prod.Production) > 0 {
		points = append(points, extractProductionStats(prod)...)
	}
	inverters, _, err := e.Inverters()
	if err != nil {
		log.Errorf("Error getting Inverter data from Envoy: %v", err)
	}
	if inverters != nil && len(*inverters) > 0 {
		points = append(points, extractInverterStats(inverters)...)
	}

	batteries, _, err := e.Batteries()
	if err != nil {
		log.Errorf("Error getting Battery data from Envoy: %v", err)
	} else {
		points = append(points, extractBatteryStats(batteries)...)
	}
	client := influxdb2.NewClient(cfg.InfluxDB, cfg.InfluxDBToken)
	writeAPI := client.WriteAPIBlocking(cfg.InfluxDBOrg, cfg.InfluxDBBucket)
	err = writeAPI.WritePoint(context.Background(), points...)
	if err != nil {
		log.Errorf("Error writing data to InfluxDB: %v", err)
	}
	return len(points)
}

func scrapeLoop() {
	log.Infof("Connecting to envoy at: %s", cfg.Address)
	connected := false
	var err error
	e := &envoy.Client{}
	interval := time.Duration(cfg.Interval) * time.Second
	for !connected {
		e, err = envoy.NewClient(cfg.Username,
			cfg.Password,
			cfg.SerialNumber,
			envoy.WithGatewayAddress(cfg.Address),
			envoy.WithDebug(true),
			envoy.WithJWT(cfg.JWT))
		if err != nil {
			log.Error(err)
			time.Sleep(interval)
		} else {
			connected = true
		}
	}
	for {
		tStat := time.Now()
		numPoints := scrape(e)
		scrapeDuration := time.Since(tStat)
		timeToSleep := time.Until(tStat.Add(interval))
		log.Infof("Scrape took: %v, found %d points, sleeping %v", scrapeDuration, numPoints, timeToSleep)
		time.Sleep(timeToSleep)
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

	cfg.Interval = 5

	log.Infof("Reading Config: %s", cfgFile)
	f, err := os.Open(cfgFile)
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&cfg)
	if err != nil {
		log.Fatalf("Error reading config: %v", err)
	}
	log.SetFormatter(&log.TextFormatter{
		FullTimestamp: true,
	})

	if cfg.Address == "" {
		log.Fatal("Missing required configuration: address")
	}
	if cfg.SerialNumber == "" {
		log.Fatal("Missing required configuration: serial")
	}

	if (cfg.Username == "" && cfg.Password == "") && cfg.JWT == "" {
		log.Fatal("Missing Envoy authentication.  Add username & password and optionally the JWT token")
	}

	if cfg.InfluxDB == "" {
		log.Fatal("Missing required configuration: influxdb")
	}
	if cfg.InfluxDBBucket == "" {
		log.Fatal("Missing required configuration: influxdb_bucket")
	}
	if cfg.InfluxDBToken == "" {
		log.Fatal("Missing required configuration: influxdb_token")
	}
	if cfg.InfluxDBOrg == "" {
		log.Fatal("Missing required configuration: influxdb_org")
	}
	log.Infof("Build with Go version: %s\n", runtime.Version())
	log.Infof("Scraping envoy at: %s with serial number %s every %d seconds", cfg.Address, cfg.SerialNumber, cfg.Interval)
	log.Infof("Writing to Influxdb: %s, Bucket '%s'", cfg.InfluxDB, cfg.InfluxDBBucket)
	scrapeLoop()
}
