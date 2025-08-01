// Copyright (C) 2019-2025 Algorand, Inc.
// This file is part of go-algorand
//
// go-algorand is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as
// published by the Free Software Foundation, either version 3 of the
// License, or (at your option) any later version.
//
// go-algorand is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with go-algorand.  If not, see <https://www.gnu.org/licenses/>.

package logging

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"

	"github.com/algorand/go-deadlock"

	"github.com/algorand/go-algorand/logging/telemetryspec"
	"github.com/algorand/go-algorand/test/partitiontest"
)

type mockTelemetryHook struct {
	mu       *deadlock.Mutex
	levels   []logrus.Level
	_entries []string
	_data    []logrus.Fields
	cb       func(entry *logrus.Entry)
}

func makeMockTelemetryHook(level logrus.Level) mockTelemetryHook {
	levels := make([]logrus.Level, 0)
	for _, l := range []logrus.Level{
		logrus.PanicLevel,
		logrus.FatalLevel,
		logrus.ErrorLevel,
		logrus.WarnLevel,
		logrus.InfoLevel,
		logrus.DebugLevel,
	} {
		if l <= level {
			levels = append(levels, l)
		}
	}
	h := mockTelemetryHook{
		levels: levels,
		mu:     &deadlock.Mutex{},
	}
	return h
}

type telemetryTestFixture struct {
	hook  mockTelemetryHook
	telem *telemetryState
	l     logger
}

func makeTelemetryTestFixture(minLevel logrus.Level) *telemetryTestFixture {
	return makeTelemetryTestFixtureWithConfig(minLevel, nil)
}

func makeTelemetryTestFixtureWithConfig(minLevel logrus.Level, cfg *TelemetryConfig) *telemetryTestFixture {
	f := &telemetryTestFixture{}
	var lcfg TelemetryConfig
	if cfg == nil {
		lcfg = createTelemetryConfig()
	} else {
		lcfg = *cfg
	}
	lcfg.Enable = true
	lcfg.MinLogLevel = minLevel
	f.hook = makeMockTelemetryHook(minLevel)
	f.l = Base().(logger)
	f.l.SetLevel(Debug) // Ensure logging doesn't filter anything out

	f.telem, _ = makeTelemetryStateContext(context.Background(), lcfg, func(ctx context.Context, cfg TelemetryConfig) (hook logrus.Hook, err error) {
		return &f.hook, nil
	})
	f.l.loggerState.telemetry = f.telem
	return f
}

func (f *telemetryTestFixture) Flush() {
	f.telem.hook.Flush()
}

func (f *telemetryTestFixture) hookData() []logrus.Fields {
	f.Flush()
	return f.hook.data()
}

func (f *telemetryTestFixture) hookEntries() []string {
	f.Flush()
	return f.hook.entries()
}

func (h *mockTelemetryHook) Levels() []logrus.Level {
	return h.levels
}

func (h *mockTelemetryHook) Fire(entry *logrus.Entry) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h._entries = append(h._entries, entry.Message)
	h._data = append(h._data, entry.Data)
	if h.cb != nil {
		h.cb(entry)
	}
	return nil
}

func (h *mockTelemetryHook) data() []logrus.Fields {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h._data
}

func (h *mockTelemetryHook) entries() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h._entries
}

func TestCreateHookError(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)

	cfg := createTelemetryConfig()
	cfg.Enable = true
	telem, err := makeTelemetryStateContext(context.Background(), cfg, func(ctx context.Context, cfg TelemetryConfig) (hook logrus.Hook, err error) {
		return nil, fmt.Errorf("failed")
	})

	a.Nil(telem)
	a.NotNil(err)
	a.Equal(err.Error(), "failed")
}

func TestTelemetryHook(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	f := makeTelemetryTestFixture(logrus.InfoLevel)

	a.NotNil(f.l.loggerState.telemetry)
	a.Zero(len(f.hookEntries()))

	f.telem.logMetrics(f.l, testString1, testMetrics{}, nil)
	f.telem.logEvent(f.l, testString1, testString2, nil)

	entries := f.hookEntries()
	a.Equal(2, len(entries))
	a.Equal(buildMessage(testString1, testString2), entries[0])
	a.Equal(buildMessage(testString1, testString2), entries[1])
}

func TestNilMetrics(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	f := makeTelemetryTestFixture(logrus.InfoLevel)

	f.telem.logMetrics(f.l, testString1, nil, nil)

	a.Zero(len(f.hookEntries()))
}

func TestDetails(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	f := makeTelemetryTestFixture(logrus.InfoLevel)

	details := testMetrics{
		val: "value",
	}
	f.telem.logEvent(f.l, testString1, testString2, details)

	data := f.hookData()
	a.NotNil(data)
	a.Equal(details, data[0]["details"])
}

func TestHeartbeatDetails(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	f := makeTelemetryTestFixture(logrus.InfoLevel)

	var hb telemetryspec.HeartbeatEventDetails
	hb.Info.Version = "v2"
	hb.Info.VersionNum = "1234"
	hb.Info.Channel = "alpha"
	hb.Info.Branch = "br0"
	hb.Info.CommitHash = "abcd"
	hb.Metrics = map[string]float64{
		"Hello": 38.8,
	}
	f.telem.logEvent(f.l, telemetryspec.ApplicationState, telemetryspec.HeartbeatEvent, hb)

	data := f.hookData()
	a.NotNil(data)
	a.Len(data, 1)
	a.Equal(hb, data[0]["details"])

	// assert JSON serialization is backwards compatible
	js, err := json.Marshal(data[0])
	a.NoError(err)
	var unjs map[string]interface{}
	a.NoError(json.Unmarshal(js, &unjs))
	a.Contains(unjs, "details")
	ev := unjs["details"].(map[string]interface{})
	Metrics := ev["Metrics"].(map[string]interface{})
	m := ev["m"].(map[string]interface{})
	a.Equal("v2", Metrics["version"].(string))
	a.Equal("1234", Metrics["version-num"].(string))
	a.Equal("alpha", Metrics["channel"].(string))
	a.Equal("br0", Metrics["branch"].(string))
	a.Equal("abcd", Metrics["commit-hash"].(string))
	a.InDelta(38.8, m["Hello"].(float64), 0.01)
}

type testMetrics struct {
	val string
}

func (m testMetrics) Identifier() telemetryspec.Metric {
	return testString2
}

func TestMetrics(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	f := makeTelemetryTestFixture(logrus.InfoLevel)

	metrics := testMetrics{
		val: "value",
	}

	f.telem.logMetrics(f.l, testString1, metrics, nil)

	data := f.hookData()
	a.NotNil(data)
	a.Equal(metrics, data[0]["metrics"])
}

func TestLogHook(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	f := makeTelemetryTestFixture(logrus.InfoLevel)

	// Wire up our telemetry hook directly
	enableTelemetryState(f.telem, &f.l)
	a.True(f.l.GetTelemetryEnabled())

	// When we enable telemetry, we no longer send an event.
	a.Equal(0, len(f.hookEntries()))

	f.l.Warn("some error")

	// Now that we're hooked, we should see the log entry in telemetry too
	a.Equal(1, len(f.hookEntries()))
}

func TestLogLevels(t *testing.T) {
	partitiontest.PartitionTest(t)
	runLogLevelsTest(t, logrus.DebugLevel, 7)
	runLogLevelsTest(t, logrus.InfoLevel, 6)
	runLogLevelsTest(t, logrus.WarnLevel, 5)
	runLogLevelsTest(t, logrus.ErrorLevel, 4)
	runLogLevelsTest(t, logrus.FatalLevel, 1)
	runLogLevelsTest(t, logrus.PanicLevel, 1)
}

func runLogLevelsTest(t *testing.T, minLevel logrus.Level, expected int) {
	a := require.New(t)
	f := makeTelemetryTestFixture(minLevel)
	enableTelemetryState(f.telem, &f.l)

	f.l.Debug("debug")
	f.l.Info("info")
	f.l.Warn("warn")
	f.l.Error("error")
	// f.l.Fatal("fatal") - can't call this - it will os.Exit()

	// Protect the call to log.Panic as we don't really want to crash
	require.Panics(t, func() {
		f.l.Panic("panic")
	})

	// See if we got the expected number of entries
	a.Equal(expected, len(f.hookEntries()))
}

func TestLogHistoryLevels(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	cfg := createTelemetryConfig()
	cfg.MinLogLevel = logrus.DebugLevel
	cfg.ReportHistoryLevel = logrus.ErrorLevel

	f := makeTelemetryTestFixtureWithConfig(logrus.DebugLevel, &cfg)
	enableTelemetryState(f.telem, &f.l)

	f.l.Debug("debug")
	f.l.Info("info")
	f.l.Warn("warn")
	f.l.Error("error")
	// f.l.Fatal("fatal") - can't call this - it will os.Exit()
	// Protect the call to log.Panic as we don't really want to crash
	require.Panics(t, func() {
		f.l.Panic("panic")
	})

	data := f.hookData()
	a.Nil(data[0]["log"]) // Debug
	a.Nil(data[1]["log"]) // Info
	a.Nil(data[2]["log"]) // Warn

	// Starting with Error level, we include log history.
	// Error also emits a debug.stack() log error, so each Error/Panic also create
	// a log entry.
	// We do not include log history with stack trace events as they're redundant

	a.Nil(data[3]["log"])    // Error - we start including log history (this is stack trace)
	a.NotNil(data[4]["log"]) // Error
	a.Nil(data[5]["log"])    // Panic - this is stack trace
	a.NotNil(data[6]["log"]) // Panic
}

func TestReadTelemetryConfigOrDefaultNoDataDir(t *testing.T) {
	partitiontest.PartitionTest(t)
	a := require.New(t)
	tempDir := os.TempDir()

	cfg, err := ReadTelemetryConfigOrDefault("", tempDir)
	defaultCfgSettings := createTelemetryConfig()

	a.Nil(err)
	a.NotNil(cfg)
	a.NotEqual(TelemetryConfig{}, cfg)
	a.Equal(defaultCfgSettings.UserName, cfg.UserName)
	a.Equal(defaultCfgSettings.Password, cfg.Password)
	a.Equal(len(defaultCfgSettings.GUID), len(cfg.GUID))
}
