/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/davecgh/go-spew/spew"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/loafoe/go-envoy"
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
	var ps []*influxdb2write.Point

	for _, inv := range *inverters {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("inverter-production-%s", inv.SerialNumber)).
			AddTag("source", cfg.SourceTag).
			AddTag("measurement-type", "inverter").
			AddTag("serial", inv.SerialNumber).
			AddField("P", inv.LastReportWatts).
			SetTime(time.Now())
		ps = append(ps, pt)
	}

	return ps
}

func extractBatteryStats(batteries *[]envoy.Battery) []*influxdb2write.Point {
	var bats []*influxdb2write.Point

	for _, inv := range *batteries {
		pt := influxdb2.NewPointWithMeasurement(fmt.Sprintf("battery-%s", inv.SerialNum)).
			AddTag("source", cfg.SourceTag).
			AddTag("measurement-type", "battery").
			AddTag("serial", inv.SerialNum).
			AddField("percent-full", inv.PercentFull).
			AddField("temperature", inv.Temperature).
			SetTime(time.Now())
		bats = append(bats, pt)
	}
	return bats
}

func scrape(e *envoy.Client) {
	log.Println("Scraping envoy")
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
		log.Fatal(err)
	}
	var points []*influxdb2write.Point
	if prod != nil && len(prod.Production) > 0 {
		points = append(points, extractProductionStats(prod)...)
	}
	inverters, _, err := e.Inverters()
	if err != nil {
		log.Fatal(err)
	}
	points = append(points, extractInverterStats(inverters)...)

	batteries, _, err := e.Batteries()
	if err != nil {
		log.Fatal(err)
	}
	points = append(points, extractBatteryStats(batteries)...)

	log.Printf("Found %d points to write\n", len(points))
	client := influxdb2.NewClient(cfg.InfluxDB, cfg.InfluxDBToken)
	writeAPI := client.WriteAPIBlocking(cfg.InfluxDBOrg, cfg.InfluxDBBucket)
	err = writeAPI.WritePoint(context.Background(), points...)
	if err != nil {
		log.Fatal(err)
	}
}

func scrapeLoop() {
	log.Infof("Connecting to envoy at: %s", cfg.Address)
	e, err := envoy.NewClient(cfg.Username,
		cfg.Password,
		cfg.SerialNumber,
		envoy.WithGatewayAddress(cfg.Address),
		envoy.WithDebug(true),
		envoy.WithJWT(cfg.JWT))
	if err != nil {
		log.Fatal(err)
	}
	interval := cfg.Interval
	for {
		tStat := time.Now()
		scrape(e)
		scrapeDuration := time.Since(tStat)
		log.Infof("Scrape took: %s", scrapeDuration)

		timeToSleep := interval - int(scrapeDuration.Seconds())
		if timeToSleep < 0 {
			timeToSleep = 0
		}
		log.Infof("Sleeping %d until next scrape", timeToSleep)
		time.Sleep(time.Duration(interval) * time.Second)
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
	flag.StringVar(&cfgFile, "config", "envoy.yaml", "Path to config file.")
	flag.Parse()

	cfg.Interval = 5

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
	spew.Dump(cfg)
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

	scrapeLoop()
}
