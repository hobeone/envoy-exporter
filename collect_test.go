package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	envoy "github.com/loafoe/go-envoy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockEnvoyClient implements EnvoyClient using configurable func fields.
type MockEnvoyClient struct {
	ProductionFunc        func() (*envoy.ProductionResponse, error)
	InvertersFunc         func() (*[]envoy.Inverter, error)
	BatteriesFunc         func() (*[]envoy.Battery, error)
	InvalidateSessionFunc func()
}

func (m *MockEnvoyClient) Production() (*envoy.ProductionResponse, error) {
	if m.ProductionFunc != nil {
		return m.ProductionFunc()
	}
	return nil, nil
}

func (m *MockEnvoyClient) Inverters() (*[]envoy.Inverter, error) {
	if m.InvertersFunc != nil {
		return m.InvertersFunc()
	}
	return nil, nil
}

func (m *MockEnvoyClient) Batteries() (*[]envoy.Battery, error) {
	if m.BatteriesFunc != nil {
		return m.BatteriesFunc()
	}
	return nil, nil
}

func (m *MockEnvoyClient) InvalidateSession() {
	if m.InvalidateSessionFunc != nil {
		m.InvalidateSessionFunc()
	}
}

// MockPointWriter captures written points for assertion.
type MockPointWriter struct {
	WritePointFunc func(ctx context.Context, point ...*influxdb2write.Point) error
	Written        []*influxdb2write.Point
}

func (m *MockPointWriter) WritePoint(ctx context.Context, point ...*influxdb2write.Point) error {
	if m.WritePointFunc != nil {
		return m.WritePointFunc(ctx, point...)
	}
	m.Written = append(m.Written, point...)
	return nil
}

func TestLineToPoint(t *testing.T) {
	line := envoy.Line{
		WNow:       100,
		ReactPwr:   200,
		ApprntPwr:  300,
		RmsCurrent: 400,
		RmsVoltage: 500,
	}
	ts := time.Now()
	pt := lineToPoint("production", line, 2, "home", ts)

	assert.Equal(t, "production-line2", pt.Name())

	tags := tagMap(pt)
	assert.Equal(t, map[string]string{
		"source":           "home",
		"measurement-type": "production",
		"line-idx":         "2",
	}, tags)

	fields := fieldMap(pt)
	assert.Equal(t, float64(100), fields["P"])
	assert.Equal(t, float64(200), fields["Q"])
	assert.Equal(t, float64(300), fields["S"])
	assert.Equal(t, float64(400), fields["I_rms"])
	assert.Equal(t, float64(500), fields["V_rms"])
}

func TestExtractProductionStats(t *testing.T) {
	prod := &envoy.ProductionResponse{
		Production: []envoy.Measurement{
			{
				MeasurementType: MeasurementProduction,
				Lines:           []envoy.Line{{WNow: 100}, {WNow: 200}},
			},
		},
		Consumption: []envoy.Measurement{
			{
				MeasurementType: MeasurementTotalConsumption,
				Lines:           []envoy.Line{{WNow: 300}},
			},
			{
				MeasurementType: MeasurementNetConsumption,
				Lines:           []envoy.Line{{WNow: 400}},
			},
		},
	}

	pts := extractProductionStats(prod, "test", time.Now())
	require.Len(t, pts, 4)
	assert.Equal(t, "production-line0", pts[0].Name())
	assert.Equal(t, "production-line1", pts[1].Name())
	assert.Equal(t, "consumption-line0", pts[2].Name())
	assert.Equal(t, "net-line0", pts[3].Name())
}

func TestExtractProductionStats_IgnoresUnknownTypes(t *testing.T) {
	prod := &envoy.ProductionResponse{
		Production: []envoy.Measurement{
			{MeasurementType: "unknown", Lines: []envoy.Line{{WNow: 50}}},
		},
	}
	pts := extractProductionStats(prod, "test", time.Now())
	assert.Empty(t, pts)
}

func TestExtractInverterStats(t *testing.T) {
	inverters := &[]envoy.Inverter{
		{SerialNumber: "ABC", LastReportWatts: 250},
		{SerialNumber: "DEF", LastReportWatts: 300},
	}
	pts := extractInverterStats(inverters, "home", time.Now())
	require.Len(t, pts, 2)
	assert.Equal(t, "inverter-production-ABC", pts[0].Name())
	assert.Equal(t, "inverter-production-DEF", pts[1].Name())
	assert.Equal(t, "inverter", tagMap(pts[0])["measurement-type"])
	assert.Equal(t, int64(250), fieldMap(pts[0])["P"])
}

func TestExtractBatteryStats(t *testing.T) {
	batteries := &[]envoy.Battery{
		{SerialNum: "BAT1", PercentFull: 80, Temperature: 25},
	}
	pts := extractBatteryStats(batteries, "home", time.Now())
	require.Len(t, pts, 1)
	assert.Equal(t, "battery-BAT1", pts[0].Name())
	assert.Equal(t, "battery", tagMap(pts[0])["measurement-type"])
	assert.Equal(t, "BAT1", tagMap(pts[0])["serial"])
	assert.Equal(t, int64(80), fieldMap(pts[0])["percent-full"])
	assert.Equal(t, int64(25), fieldMap(pts[0])["temperature"])
}

func TestScrape_AllSources(t *testing.T) {
	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
		InvertersFunc: func() (*[]envoy.Inverter, error) {
			return &[]envoy.Inverter{{SerialNumber: "S1", LastReportWatts: 50}}, nil
		},
		BatteriesFunc: func() (*[]envoy.Battery, error) {
			return &[]envoy.Battery{{SerialNum: "B1", PercentFull: 90}}, nil
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 3, result.points)
	assert.False(t, result.hasErr)
}

func TestScrape_ProductionError(t *testing.T) {
	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return nil, errors.New("network error")
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 0, result.points)
	assert.True(t, result.hasErr)
}

func TestScrape_WriteError(t *testing.T) {
	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
	}
	writer := &MockPointWriter{
		WritePointFunc: func(ctx context.Context, point ...*influxdb2write.Point) error {
			return errors.New("influxdb unavailable")
		},
	}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 0, result.points, "write failed so points should not be counted")
	assert.True(t, result.hasErr)
}

func TestScrape_PartialErrors(t *testing.T) {
	// Production succeeds, inverters fail — expect production points written.
	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
		InvertersFunc: func() (*[]envoy.Inverter, error) {
			return nil, errors.New("inverter error")
		},
		BatteriesFunc: func() (*[]envoy.Battery, error) {
			return nil, errors.New("battery error")
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 1, result.points)
	assert.True(t, result.hasErr)
}

func TestConnectWithBackoff_ImmediateSuccess(t *testing.T) {
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) { return client, nil }

	ctx := context.Background()
	e, err := connectWithBackoff(ctx, &Config{}, factory, 10*time.Millisecond, 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, client, e)
}

func TestConnectWithBackoff_RetriesThenSucceeds(t *testing.T) {
	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	ctx := context.Background()
	e, err := connectWithBackoff(ctx, &Config{}, factory, 5*time.Millisecond, 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, client, e)
	assert.Equal(t, 3, attempts)
}

func TestConnectWithBackoff_CancelledContext(t *testing.T) {
	factory := func(_ *Config) (EnvoyClient, error) {
		return nil, errors.New("always fails")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := connectWithBackoff(ctx, &Config{}, factory, 10*time.Millisecond, 1*time.Second)
	assert.Error(t, err)
}

func TestScrapeLoop_RunsAndUpdatesLastScrapeTime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	client := &MockEnvoyClient{
		ProductionFunc: func() (*envoy.ProductionResponse, error) {
			return &envoy.ProductionResponse{
				Production: []envoy.Measurement{
					{MeasurementType: MeasurementProduction, Lines: []envoy.Line{{WNow: 100}}},
				},
			}, nil
		},
	}
	writer := &MockPointWriter{}
	factory := func(_ *Config) (EnvoyClient, error) { return client, nil }

	var lastScrapeTime atomic.Int64
	cfg := &Config{Interval: 1, RetryInterval: 1}

	scrapeLoop(ctx, cfg, writer, factory, &lastScrapeTime, nil)

	assert.Greater(t, lastScrapeTime.Load(), int64(0), "last scrape time should be set")
}

func TestScrapeLoop_ConnectionRetry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 2 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	var lastScrapeTime atomic.Int64
	cfg := &Config{Interval: 1, RetryInterval: 0} // 0 → uses 5s default but gets overridden in test

	// Use a very short base retry for the test
	// We can't pass base directly to scrapeLoop, but RetryInterval=0 defaults to 5s.
	// Use 1s RetryInterval so retry finishes within the 2s timeout.
	cfg.RetryInterval = 1

	scrapeLoop(ctx, cfg, &MockPointWriter{}, factory, &lastScrapeTime, nil)

	assert.GreaterOrEqual(t, attempts, 2)
}

// helpers

func tagMap(pt *influxdb2write.Point) map[string]string {
	m := make(map[string]string)
	for _, tag := range pt.TagList() {
		m[tag.Key] = tag.Value
	}
	return m
}

func fieldMap(pt *influxdb2write.Point) map[string]interface{} {
	m := make(map[string]interface{})
	for _, f := range pt.FieldList() {
		m[f.Key] = f.Value
	}
	return m
}
