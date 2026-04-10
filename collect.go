package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	envoy "github.com/loafoe/go-envoy"
)

const (
	// Measurement names written to InfluxDB.
	MeasurementProduction       = "production"
	MeasurementTotalConsumption = "total-consumption"
	MeasurementNetConsumption   = "net-consumption"
	MeasurementInverter         = "inverter"
	MeasurementBattery          = "battery"

	// Tag keys.
	TagSource          = "source"
	TagMeasurementType = "measurement-type"
	TagLineIdx         = "line-idx"
	TagSerial          = "serial"

	// Field keys.
	FieldP           = "P"
	FieldQ           = "Q"
	FieldS           = "S"
	FieldIrms        = "I_rms"
	FieldVrms        = "V_rms"
	FieldPercentFull = "percent-full"
	FieldTemperature = "temperature"
)

// EnvoyClient defines the methods for interacting with the Envoy gateway.
type EnvoyClient interface {
	Production() (*envoy.ProductionResponse, error)
	Inverters() (*[]envoy.Inverter, error)
	Batteries() (*[]envoy.Battery, error)
	InvalidateSession()
}

// PointWriter abstracts the InfluxDB blocking write API for testability.
type PointWriter interface {
	WritePoint(ctx context.Context, point ...*influxdb2write.Point) error
}

// ClientFactory creates an EnvoyClient from a Config.
type ClientFactory func(cfg *Config) (EnvoyClient, error)

// scrapeResult summarises the outcome of a single scrape iteration.
type scrapeResult struct {
	points int
	hasErr bool
}

// lineToPoint builds an InfluxDB point for a single phase line.
// namePrefix forms the measurement name ("<namePrefix>-line<idx>").
// typeTag is written as the measurement-type tag value.
func lineToPoint(namePrefix, typeTag string, line envoy.Line, idx int, sourceTag string, t time.Time) *influxdb2write.Point {
	return influxdb2.NewPointWithMeasurement(fmt.Sprintf("%s-line%d", namePrefix, idx)).
		AddTag(TagSource, sourceTag).
		AddTag(TagMeasurementType, typeTag).
		AddTag(TagLineIdx, strconv.Itoa(idx)).
		AddField(FieldP, line.WNow).
		AddField(FieldQ, line.ReactPwr).
		AddField(FieldS, line.ApprntPwr).
		AddField(FieldIrms, line.RmsCurrent).
		AddField(FieldVrms, line.RmsVoltage).
		SetTime(t)
}

func extractProductionStats(prod *envoy.ProductionResponse, sourceTag string, t time.Time) []*influxdb2write.Point {
	var ps []*influxdb2write.Point
	for _, m := range prod.Production {
		if m.MeasurementType == MeasurementProduction {
			for i, line := range m.Lines {
				ps = append(ps, lineToPoint(MeasurementProduction, MeasurementProduction, line, i, sourceTag, t))
			}
		}
	}
	for _, m := range prod.Consumption {
		switch m.MeasurementType {
		case MeasurementTotalConsumption:
			for i, line := range m.Lines {
				ps = append(ps, lineToPoint("consumption", MeasurementTotalConsumption, line, i, sourceTag, t))
			}
		case MeasurementNetConsumption:
			for i, line := range m.Lines {
				ps = append(ps, lineToPoint("net", MeasurementNetConsumption, line, i, sourceTag, t))
			}
		}
	}
	return ps
}

func extractInverterStats(inverters *[]envoy.Inverter, sourceTag string, t time.Time) []*influxdb2write.Point {
	ps := make([]*influxdb2write.Point, len(*inverters))
	for i, inv := range *inverters {
		ps[i] = influxdb2.NewPointWithMeasurement(fmt.Sprintf("inverter-production-%s", inv.SerialNumber)).
			AddTag(TagSource, sourceTag).
			AddTag(TagMeasurementType, MeasurementInverter).
			AddTag(TagSerial, inv.SerialNumber).
			AddField(FieldP, inv.LastReportWatts).
			SetTime(t)
	}
	return ps
}

func extractBatteryStats(batteries *[]envoy.Battery, sourceTag string, t time.Time) []*influxdb2write.Point {
	ps := make([]*influxdb2write.Point, len(*batteries))
	for i, bat := range *batteries {
		ps[i] = influxdb2.NewPointWithMeasurement(fmt.Sprintf("battery-%s", bat.SerialNum)).
			AddTag(TagSource, sourceTag).
			AddTag(TagMeasurementType, MeasurementBattery).
			AddTag(TagSerial, bat.SerialNum).
			AddField(FieldPercentFull, bat.PercentFull).
			AddField(FieldTemperature, bat.Temperature).
			SetTime(t)
	}
	return ps
}

// logPoint emits a Debug-level log entry for a single InfluxDB point,
// grouping tags and fields for clean structured output.
func logPoint(pt *influxdb2write.Point) {
	tagArgs := make([]any, 0, len(pt.TagList())*2)
	for _, tag := range pt.TagList() {
		tagArgs = append(tagArgs, tag.Key, tag.Value)
	}

	fieldArgs := make([]any, 0, len(pt.FieldList())*2)
	for _, field := range pt.FieldList() {
		fieldArgs = append(fieldArgs, field.Key, field.Value)
	}

	slog.Debug("write point",
		"measurement", pt.Name(),
		"time", pt.Time(),
		slog.Group("tags", tagArgs...),
		slog.Group("fields", fieldArgs...),
	)
}

// scrape fetches data from all Envoy endpoints and writes points to InfluxDB.
// Errors from individual endpoints are logged but do not abort the scrape.
func scrape(ctx context.Context, e EnvoyClient, writeAPI PointWriter, sourceTag string) scrapeResult {
	var points []*influxdb2write.Point
	var hasErr bool

	// Capture a single timestamp so all points in this scrape share the same time.
	scrapeTime := time.Now()

	t := scrapeTime
	prod, err := e.Production()
	dur := time.Since(t)
	if err != nil {
		slog.Error("Production fetch failed", "error", err, "duration", dur)
		hasErr = true
	} else {
		var prodPts []*influxdb2write.Point
		if prod != nil {
			prodPts = extractProductionStats(prod, sourceTag, scrapeTime)
		}
		slog.Debug("Production fetch", "duration", dur, "points", len(prodPts))
		points = append(points, prodPts...)
	}

	t = time.Now()
	inverters, err := e.Inverters()
	dur = time.Since(t)
	if err != nil {
		slog.Error("Inverters fetch failed", "error", err, "duration", dur)
		hasErr = true
	} else {
		var count int
		if inverters != nil {
			count = len(*inverters)
		}
		slog.Debug("Inverters fetch", "duration", dur, "inverters", count)
		if count > 0 {
			points = append(points, extractInverterStats(inverters, sourceTag, scrapeTime)...)
		}
	}

	t = time.Now()
	batteries, err := e.Batteries()
	dur = time.Since(t)
	if err != nil {
		slog.Error("Batteries fetch failed", "error", err, "duration", dur)
		hasErr = true
	} else {
		count := 0
		if batteries != nil {
			count = len(*batteries)
		}
		slog.Debug("Batteries fetch", "duration", dur, "batteries", count)
		if count > 0 {
			points = append(points, extractBatteryStats(batteries, sourceTag, scrapeTime)...)
		}
	}

	if len(points) > 0 {
		writeCtx, writeCancel := context.WithTimeout(ctx, 30*time.Second)
		defer writeCancel()
		if slog.Default().Enabled(writeCtx, slog.LevelDebug) {
			for _, pt := range points {
				logPoint(pt)
			}
		}
		t = time.Now()
		if err := writeAPI.WritePoint(writeCtx, points...); err != nil {
			slog.Error("InfluxDB write failed",
				"error", err,
				"points", len(points),
				"duration", time.Since(t))
			hasErr = true
			points = nil // write failed; don't count as written
		} else {
			slog.Debug("InfluxDB write", "duration", time.Since(t), "points", len(points))
		}
	}

	return scrapeResult{points: len(points), hasErr: hasErr}
}

// connectWithBackoff retries clientFactory with exponential backoff until
// a client is created successfully or ctx is cancelled.
func connectWithBackoff(ctx context.Context, cfg *Config, factory ClientFactory, base, maxDelay time.Duration) (EnvoyClient, error) {
	backoff := base
	for {
		e, err := factory(cfg)
		if err == nil {
			return e, nil
		}
		slog.Error("Failed to connect to Envoy", "error", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxDelay)
	}
}

// scrapeLoop connects to the Envoy gateway and scrapes on a regular interval.
// reconnect, if non-nil, triggers a reconnect when it receives a signal (e.g. JWT refresh).
func scrapeLoop(ctx context.Context, cfg *Config, writeAPI PointWriter, factory ClientFactory, reconnect <-chan struct{}) {
	slog.Info("Connecting to Envoy", "address", cfg.Address)

	baseRetry := time.Duration(cfg.RetryInterval) * time.Second
	if baseRetry == 0 {
		baseRetry = 5 * time.Second
	}

	e, err := connectWithBackoff(ctx, cfg, factory, baseRetry, 5*time.Minute)
	if err != nil {
		return // context cancelled before we connected
	}

	ticker := time.NewTicker(time.Duration(cfg.Interval) * time.Second)
	defer ticker.Stop()

	interval := time.Duration(cfg.Interval) * time.Second

	// doScrapeAt runs one scrape and logs how long until the next tick.
	// tickAt is when the triggering tick fired; it anchors the "next in" calculation
	// so that scrape duration does not skew the reported wait time.
	doScrapeAt := func(tickAt time.Time) {
		start := time.Now()
		result := scrape(ctx, e, writeAPI, cfg.SourceTag)
		dur := time.Since(start)

		nextIn := max(time.Until(tickAt.Add(interval)).Truncate(time.Second), 0)
		slog.Info("Scrape finished",
			"duration", dur,
			"points", result.points,
			"errors", result.hasErr,
			"next_in", nextIn)
	}

	doScrapeAt(time.Now()) // immediate first scrape

	for {
		select {
		case <-ctx.Done():
			slog.Info("Stopping scrape loop")
			return
		case <-reconnect: // nil channel blocks forever; fires only when JWT is refreshed
			slog.Info("JWT refreshed; reconnecting to Envoy")
			newClient, err := connectWithBackoff(ctx, cfg, factory, baseRetry, 5*time.Minute)
			if err != nil {
				return
			}
			e = newClient
		case t := <-ticker.C:
			doScrapeAt(t)
		}
	}
}
