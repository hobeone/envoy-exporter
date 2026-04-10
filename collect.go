package main

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	gateway "github.com/hobeone/enphase-gateway"
	influxdb2 "github.com/influxdata/influxdb-client-go/v2"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
)

const (
	// Measurement names written to InfluxDB.
	MeasurementProduction       = "production"
	MeasurementTotalConsumption = "total-consumption"
	MeasurementNetConsumption   = "net-consumption"
	MeasurementInverter         = "inverter"

	// Tag keys.
	TagSource          = "source"
	TagMeasurementType = "measurement-type"
	TagLineIdx         = "line-idx"
	TagSerial          = "serial"

	// Field keys.
	FieldP    = "P"
	FieldQ    = "Q"
	FieldS    = "S"
	FieldIrms = "I_rms"
	FieldVrms = "V_rms"
)

// TypedCTReading pairs a raw CT reading with the measurement type
// (production, net-consumption, total-consumption) inferred from the
// gateway's meter configuration.
type TypedCTReading struct {
	gateway.CTReading
	MeasurementType string
}

// EnvoyClient defines the methods for interacting with the Envoy gateway.
type EnvoyClient interface {
	LiveData(ctx context.Context) (gateway.LiveData, error)
	Inverters(ctx context.Context) ([]gateway.InverterReading, error)
	TypedMeterReadings(ctx context.Context) ([]TypedCTReading, error)
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

// extractLiveDataPoints converts a LiveData response into a single energy-snapshot
// InfluxDB point capturing solar/battery/grid/load flows and battery state.
func extractLiveDataPoints(live gateway.LiveData, sourceTag string, t time.Time) []*influxdb2write.Point {
	snap := gateway.SnapshotFromLiveData(live)
	pt := influxdb2.NewPointWithMeasurement("energy-snapshot").
		AddTag(TagSource, sourceTag).
		AddField("solar_w", snap.SolarW).
		AddField("battery_w", snap.BatteryW).
		AddField("grid_w", snap.GridW).
		AddField("load_w", snap.LoadW).
		AddField("battery_soc", snap.BatterySOC).
		AddField("battery_wh", snap.BatteryWh).
		AddField("solar_to_load_w", snap.SolarToLoad).
		AddField("solar_to_grid_w", snap.SolarToGrid).
		AddField("solar_to_batt_w", snap.SolarToBatt).
		AddField("grid_to_load_w", snap.GridToLoad).
		AddField("batt_to_load_w", snap.BattToLoad).
		SetTime(t)
	return []*influxdb2write.Point{pt}
}

// ctChannelToPoint builds an InfluxDB point for a single CT phase channel.
func ctChannelToPoint(namePrefix, typeTag string, ch gateway.CTChannel, idx int, sourceTag string, t time.Time) *influxdb2write.Point {
	return influxdb2.NewPointWithMeasurement(fmt.Sprintf("%s-line%d", namePrefix, idx)).
		AddTag(TagSource, sourceTag).
		AddTag(TagMeasurementType, typeTag).
		AddTag(TagLineIdx, strconv.Itoa(idx)).
		AddField(FieldP, ch.ActivePower).
		AddField(FieldQ, ch.ReactivePower).
		AddField(FieldS, ch.ApparentPower).
		AddField(FieldIrms, ch.Current).
		AddField(FieldVrms, ch.Voltage).
		SetTime(t)
}

// extractCTPoints converts typed CT readings into per-phase InfluxDB points.
// Measurement-name prefixes follow the existing InfluxDB schema:
//
//	"production"        → production-line<N>
//	"total-consumption" → consumption-line<N>
//	"net-consumption"   → net-line<N>
func extractCTPoints(readings []TypedCTReading, sourceTag string, t time.Time) []*influxdb2write.Point {
	var ps []*influxdb2write.Point
	for _, r := range readings {
		var namePrefix string
		switch r.MeasurementType {
		case MeasurementProduction:
			namePrefix = MeasurementProduction
		case MeasurementTotalConsumption:
			namePrefix = "consumption"
		case MeasurementNetConsumption:
			namePrefix = "net"
		default:
			continue // unknown type; skip
		}
		for i, ch := range r.Channels {
			ps = append(ps, ctChannelToPoint(namePrefix, r.MeasurementType, ch, i, sourceTag, t))
		}
	}
	return ps
}

// extractInverterPoints builds one InfluxDB point per microinverter.
func extractInverterPoints(inverters []gateway.InverterReading, sourceTag string, t time.Time) []*influxdb2write.Point {
	ps := make([]*influxdb2write.Point, len(inverters))
	for i, inv := range inverters {
		ps[i] = influxdb2.NewPointWithMeasurement(fmt.Sprintf("inverter-production-%s", inv.SerialNumber)).
			AddTag(TagSource, sourceTag).
			AddTag(TagMeasurementType, MeasurementInverter).
			AddTag(TagSerial, inv.SerialNumber).
			AddField(FieldP, float64(inv.LastReportWatts)).
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
// A 404 from the CT meter endpoint is treated as a non-error (no CTs installed).
func scrape(ctx context.Context, e EnvoyClient, writeAPI PointWriter, sourceTag string) scrapeResult {
	var points []*influxdb2write.Point
	var hasErr bool

	// Capture a single timestamp so all points in this scrape share the same time.
	scrapeTime := time.Now()

	t := scrapeTime
	live, err := e.LiveData(ctx)
	dur := time.Since(t)
	if err != nil {
		slog.Error("LiveData fetch failed", "error", err, "duration", dur)
		hasErr = true
	} else {
		pts := extractLiveDataPoints(live, sourceTag, scrapeTime)
		slog.Debug("LiveData fetch", "duration", dur, "points", len(pts))
		points = append(points, pts...)
	}

	t = time.Now()
	ctReadings, err := e.TypedMeterReadings(ctx)
	dur = time.Since(t)
	if err != nil {
		if gateway.IsNotFound(err) {
			slog.Debug("No CT meters installed; skipping meter readings")
		} else {
			slog.Error("MeterReadings fetch failed", "error", err, "duration", dur)
			hasErr = true
		}
	} else {
		pts := extractCTPoints(ctReadings, sourceTag, scrapeTime)
		slog.Debug("MeterReadings fetch", "duration", dur, "points", len(pts))
		points = append(points, pts...)
	}

	t = time.Now()
	inverters, err := e.Inverters(ctx)
	dur = time.Since(t)
	if err != nil {
		slog.Error("Inverters fetch failed", "error", err, "duration", dur)
		hasErr = true
	} else {
		slog.Debug("Inverters fetch", "duration", dur, "inverters", len(inverters))
		if len(inverters) > 0 {
			points = append(points, extractInverterPoints(inverters, sourceTag, scrapeTime)...)
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
