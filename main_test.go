package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	envoy "github.com/loafoe/go-envoy"
)

func TestLineToPoint(t *testing.T) {
	cfg.SourceTag = "test"
	line := envoy.Line{
		WNow:       100,
		ReactPwr:   200,
		ApprntPwr:  300,
		RmsCurrent: 400,
		RmsVoltage: 500,
	}
	point := lineToPoint("test-type", line, 1)
	assert.NotNil(t, point)
	assert.Equal(t, "test-type-line1", point.Name())

	tags := make(map[string]string)
	for _, tag := range point.TagList() {
		tags[tag.Key] = tag.Value
	}
	assert.Equal(t, map[string]string{
		"source":           "test",
		"measurement-type": "test-type",
		"line-idx":         "1",
	}, tags)

	fields := make(map[string]interface{})
	for _, field := range point.FieldList() {
		fields[field.Key] = field.Value
	}
	assert.Equal(t, map[string]interface{}{
		"P":     float64(100),
		"Q":     float64(200),
		"S":     float64(300),
		"I_rms": float64(400),
		"V_rms": float64(500),
	}, fields)
}

func TestExtractProductionStats(t *testing.T) {
	cfg.SourceTag = "test"
	prod := &envoy.ProductionResponse{
		Production: []envoy.Measurement{
			{
				MeasurementType: "production",
				Lines: []envoy.Line{
					{WNow: 100},
				},
			},
		},
		Consumption: []envoy.Measurement{
			{
				MeasurementType: "total-consumption",
				Lines: []envoy.Line{
					{WNow: 200},
				},
			},
			{
				MeasurementType: "net-consumption",
				Lines: []envoy.Line{
					{WNow: 300},
				},
			},
		},
	}
	points := extractProductionStats(prod)
	assert.Len(t, points, 3)
	assert.Equal(t, "production-line0", points[0].Name())
	assert.Equal(t, "consumption-line0", points[1].Name())
	assert.Equal(t, "net-line0", points[2].Name())
}

func TestExtractInverterStats(t *testing.T) {
	cfg.SourceTag = "test"
	inverters := &[]envoy.Inverter{
		{
			SerialNumber:    "123",
			LastReportWatts: 100,
		},
	}
	points := extractInverterStats(inverters)
	assert.Len(t, points, 1)
	assert.Equal(t, "inverter-production-123", points[0].Name())

	tags := make(map[string]string)
	for _, tag := range points[0].TagList() {
		tags[tag.Key] = tag.Value
	}
	assert.Equal(t, map[string]string{
		"source":           "test",
		"measurement-type": "inverter",
		"serial":           "123",
	}, tags)

	fields := make(map[string]interface{})
	for _, field := range points[0].FieldList() {
		fields[field.Key] = field.Value
	}
	assert.Equal(t, map[string]interface{}{
		"P": int64(100),
	}, fields)
}

func TestExtractBatteryStats(t *testing.T) {
	cfg.SourceTag = "test"
	batteries := &[]envoy.Battery{
		{
			SerialNum:   "456",
			PercentFull: 80,
			Temperature: 25,
		},
	}
	points := extractBatteryStats(batteries)
	assert.Len(t, points, 1)
	assert.Equal(t, "battery-456", points[0].Name())

	tags := make(map[string]string)
	for _, tag := range points[0].TagList() {
		tags[tag.Key] = tag.Value
	}
	assert.Equal(t, map[string]string{
		"source":           "test",
		"measurement-type": "battery",
		"serial":           "456",
	}, tags)

	fields := make(map[string]interface{})
	for _, field := range points[0].FieldList() {
		fields[field.Key] = field.Value
	}
	assert.Equal(t, map[string]interface{}{
		"percent-full": int64(80),
		"temperature":  int64(25),
	}, fields)
}
