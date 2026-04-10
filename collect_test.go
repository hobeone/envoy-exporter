package main

import (
	"context"
	"errors"
	"testing"
	"time"

	gateway "github.com/hobeone/enphase-gateway"
	influxdb2write "github.com/influxdata/influxdb-client-go/v2/api/write"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockEnvoyClient implements EnvoyClient using configurable func fields.
type MockEnvoyClient struct {
	LiveDataFunc           func(ctx context.Context) (gateway.LiveData, error)
	InvertersFunc          func(ctx context.Context) ([]gateway.InverterReading, error)
	TypedMeterReadingsFunc func(ctx context.Context) ([]TypedCTReading, error)
}

func (m *MockEnvoyClient) LiveData(ctx context.Context) (gateway.LiveData, error) {
	if m.LiveDataFunc != nil {
		return m.LiveDataFunc(ctx)
	}
	return gateway.LiveData{}, nil
}

func (m *MockEnvoyClient) Inverters(ctx context.Context) ([]gateway.InverterReading, error) {
	if m.InvertersFunc != nil {
		return m.InvertersFunc(ctx)
	}
	return nil, nil
}

func (m *MockEnvoyClient) TypedMeterReadings(ctx context.Context) ([]TypedCTReading, error) {
	if m.TypedMeterReadingsFunc != nil {
		return m.TypedMeterReadingsFunc(ctx)
	}
	return nil, nil
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

// makeLiveData builds a LiveData value with the given aggregate power readings (milliwatts).
func makeLiveData(solarMW, battMW, gridMW, loadMW int64) gateway.LiveData {
	return gateway.LiveData{
		Meters: gateway.LiveMeters{
			LastUpdate: time.Now().Unix(),
			PV:         gateway.MeterSummary{AggPowerMW: solarMW},
			Storage:    gateway.MeterSummary{AggPowerMW: battMW},
			Grid:       gateway.MeterSummary{AggPowerMW: gridMW},
			Load:       gateway.MeterSummary{AggPowerMW: loadMW},
		},
	}
}

func TestExtractLiveDataPoints(t *testing.T) {
	t.Parallel()

	live := gateway.LiveData{
		Meters: gateway.LiveMeters{
			LastUpdate:   time.Now().Unix(),
			PV:           gateway.MeterSummary{AggPowerMW: 5000000},  // 5000 W solar
			Storage:      gateway.MeterSummary{AggPowerMW: -2000000}, // -2000 W charging
			Grid:         gateway.MeterSummary{AggPowerMW: -3000000}, // -3000 W exporting
			Load:         gateway.MeterSummary{AggPowerMW: 0},
			EncAggSOC:    85,
			EncAggEnergy: 10000,
		},
	}
	pts := extractLiveDataPoints(live, "test", time.Now())
	require.Len(t, pts, 1)
	assert.Equal(t, "energy-snapshot", pts[0].Name())
	assert.Equal(t, "test", tagMap(pts[0])["source"])

	fields := fieldMap(pts[0])
	assert.Equal(t, 5000.0, fields["solar_w"])
	assert.Equal(t, -2000.0, fields["battery_w"])
	assert.Equal(t, -3000.0, fields["grid_w"])
	assert.Equal(t, int64(85), fields["battery_soc"])
	assert.Equal(t, int64(10000), fields["battery_wh"])
	// Derived flow: exporting 3000 W surplus solar to grid.
	assert.Equal(t, 3000.0, fields["solar_to_grid_w"])
}

func TestExtractCTPoints(t *testing.T) {
	t.Parallel()

	readings := []TypedCTReading{
		{
			CTReading: gateway.CTReading{
				Channels: []gateway.CTChannel{
					{ActivePower: 100, ReactivePower: 10, ApparentPower: 110, Current: 0.5, Voltage: 240},
					{ActivePower: 200, ReactivePower: 20, ApparentPower: 220, Current: 0.8, Voltage: 240},
				},
			},
			MeasurementType: MeasurementProduction,
		},
		{
			CTReading:       gateway.CTReading{Channels: []gateway.CTChannel{{ActivePower: 300}}},
			MeasurementType: MeasurementTotalConsumption,
		},
		{
			CTReading:       gateway.CTReading{Channels: []gateway.CTChannel{{ActivePower: 400}}},
			MeasurementType: MeasurementNetConsumption,
		},
	}

	pts := extractCTPoints(readings, "home", time.Now())
	require.Len(t, pts, 4) // 2 production channels + 1 total-consumption + 1 net-consumption

	assert.Equal(t, "production-line0", pts[0].Name())
	assert.Equal(t, "production-line1", pts[1].Name())
	assert.Equal(t, "consumption-line0", pts[2].Name())
	assert.Equal(t, "net-line0", pts[3].Name())

	// Measurement-type tag must carry the full constant, not the name prefix.
	// This is the key invariant: "consumption" prefix → "total-consumption" tag.
	assert.Equal(t, MeasurementProduction, tagMap(pts[0])["measurement-type"])
	assert.Equal(t, MeasurementTotalConsumption, tagMap(pts[2])["measurement-type"])
	assert.Equal(t, MeasurementNetConsumption, tagMap(pts[3])["measurement-type"])

	assert.Equal(t, "home", tagMap(pts[0])["source"])
	assert.Equal(t, 100.0, fieldMap(pts[0])["P"])
	assert.Equal(t, 10.0, fieldMap(pts[0])["Q"])
}

func TestExtractCTPoints_SkipsUnknownType(t *testing.T) {
	t.Parallel()

	readings := []TypedCTReading{
		{
			CTReading:       gateway.CTReading{Channels: []gateway.CTChannel{{ActivePower: 50}}},
			MeasurementType: "unknown-type",
		},
	}
	pts := extractCTPoints(readings, "test", time.Now())
	assert.Empty(t, pts)
}

func TestExtractInverterPoints(t *testing.T) {
	t.Parallel()

	inverters := []gateway.InverterReading{
		{SerialNumber: "ABC", LastReportWatts: 250},
		{SerialNumber: "DEF", LastReportWatts: 300},
	}
	pts := extractInverterPoints(inverters, "home", time.Now())
	require.Len(t, pts, 2)
	assert.Equal(t, "inverter-production-ABC", pts[0].Name())
	assert.Equal(t, "inverter-production-DEF", pts[1].Name())
	assert.Equal(t, MeasurementInverter, tagMap(pts[0])["measurement-type"])
	assert.Equal(t, "ABC", tagMap(pts[0])["serial"])
	assert.Equal(t, 250.0, fieldMap(pts[0])["P"])
}

func TestScrape_AllSources(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{
		LiveDataFunc: func(_ context.Context) (gateway.LiveData, error) {
			return makeLiveData(1000000, 0, 0, 1000000), nil
		},
		InvertersFunc: func(_ context.Context) ([]gateway.InverterReading, error) {
			return []gateway.InverterReading{{SerialNumber: "S1", LastReportWatts: 50}}, nil
		},
		TypedMeterReadingsFunc: func(_ context.Context) ([]TypedCTReading, error) {
			return []TypedCTReading{{
				CTReading:       gateway.CTReading{Channels: []gateway.CTChannel{{ActivePower: 100}}},
				MeasurementType: MeasurementProduction,
			}}, nil
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 3, result.points) // 1 energy-snapshot + 1 inverter + 1 CT channel
	assert.False(t, result.hasErr)
}

func TestScrape_LiveDataError(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{
		LiveDataFunc: func(_ context.Context) (gateway.LiveData, error) {
			return gateway.LiveData{}, errors.New("network error")
		},
	}
	result := scrape(context.Background(), client, &MockPointWriter{}, "test")
	assert.Equal(t, 0, result.points)
	assert.True(t, result.hasErr)
}

func TestScrape_WriteError(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{
		LiveDataFunc: func(_ context.Context) (gateway.LiveData, error) {
			return makeLiveData(1000000, 0, 0, 1000000), nil
		},
	}
	writer := &MockPointWriter{
		WritePointFunc: func(_ context.Context, _ ...*influxdb2write.Point) error {
			return errors.New("influxdb unavailable")
		},
	}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 0, result.points, "write failed so points should not be counted")
	assert.True(t, result.hasErr)
}

func TestScrape_PartialErrors(t *testing.T) {
	t.Parallel()

	// LiveData succeeds, inverters and CT meters fail — only snapshot written.
	client := &MockEnvoyClient{
		LiveDataFunc: func(_ context.Context) (gateway.LiveData, error) {
			return makeLiveData(1000000, 0, 0, 1000000), nil
		},
		InvertersFunc: func(_ context.Context) ([]gateway.InverterReading, error) {
			return nil, errors.New("inverter error")
		},
		TypedMeterReadingsFunc: func(_ context.Context) ([]TypedCTReading, error) {
			return nil, errors.New("ct error")
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 1, result.points) // only the energy-snapshot point
	assert.True(t, result.hasErr)
}

func TestScrape_CTNotFound(t *testing.T) {
	t.Parallel()

	// CT returns 404 (no CTs installed) — should not be counted as an error.
	client := &MockEnvoyClient{
		LiveDataFunc: func(_ context.Context) (gateway.LiveData, error) {
			return makeLiveData(1000000, 0, 0, 1000000), nil
		},
		TypedMeterReadingsFunc: func(_ context.Context) ([]TypedCTReading, error) {
			return nil, &gateway.Error{StatusCode: 404, Endpoint: "/ivp/meters/readings"}
		},
	}
	writer := &MockPointWriter{}

	result := scrape(context.Background(), client, writer, "test")
	assert.Equal(t, 1, result.points)
	assert.False(t, result.hasErr, "404 from CT endpoint should not be treated as an error")
}

func TestConnectWithBackoff_ImmediateSuccess(t *testing.T) {
	t.Parallel()

	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) { return client, nil }

	e, err := connectWithBackoff(context.Background(), &Config{}, factory, 10*time.Millisecond, 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, client, e)
}

func TestConnectWithBackoff_RetriesThenSucceeds(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	e, err := connectWithBackoff(context.Background(), &Config{}, factory, 5*time.Millisecond, 1*time.Second)
	require.NoError(t, err)
	assert.Equal(t, client, e)
	assert.Equal(t, 3, attempts)
}

// TestConnectWithBackoff_BackoffCap verifies that the delay is capped at maxDelay.
func TestConnectWithBackoff_BackoffCap(t *testing.T) {
	t.Parallel()

	attempts := 0
	client := &MockEnvoyClient{}
	factory := func(_ *Config) (EnvoyClient, error) {
		attempts++
		if attempts < 5 {
			return nil, errors.New("not ready")
		}
		return client, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	e, err := connectWithBackoff(ctx, &Config{}, factory, 2*time.Millisecond, 3*time.Millisecond)
	require.NoError(t, err)
	assert.Equal(t, client, e)
	assert.Equal(t, 5, attempts)
}

func TestConnectWithBackoff_CancelledContext(t *testing.T) {
	t.Parallel()

	factory := func(_ *Config) (EnvoyClient, error) {
		return nil, errors.New("always fails")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := connectWithBackoff(ctx, &Config{}, factory, 10*time.Millisecond, 1*time.Second)
	assert.Error(t, err)
}

func TestScrapeLoop_RunsAndWritesPoints(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	client := &MockEnvoyClient{
		LiveDataFunc: func(_ context.Context) (gateway.LiveData, error) {
			return makeLiveData(1000000, 0, 0, 1000000), nil
		},
	}
	writer := &MockPointWriter{}
	factory := func(_ *Config) (EnvoyClient, error) { return client, nil }

	scrapeLoop(ctx, cfg1(), writer, factory, nil)

	assert.NotEmpty(t, writer.Written, "scrape loop should have written points")
}

func TestScrapeLoop_ConnectionRetry(t *testing.T) {
	t.Parallel()

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

	scrapeLoop(ctx, cfg1(), &MockPointWriter{}, factory, nil)

	assert.GreaterOrEqual(t, attempts, 2)
}

// cfg1 returns a minimal Config suitable for scrapeLoop tests.
func cfg1() *Config { return &Config{Interval: 1, RetryInterval: 1} }

// helpers

func tagMap(pt *influxdb2write.Point) map[string]string {
	m := make(map[string]string)
	for _, tag := range pt.TagList() {
		m[tag.Key] = tag.Value
	}
	return m
}

func fieldMap(pt *influxdb2write.Point) map[string]any {
	m := make(map[string]any)
	for _, f := range pt.FieldList() {
		m[f.Key] = f.Value
	}
	return m
}
