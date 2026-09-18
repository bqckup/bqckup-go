package report

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDailyPayloadIncludesServerIDAndDiagnosticFields(t *testing.T) {
	data := DailyReportData{
		Date: time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC),
	}
	payload := dailyPayload(data, "ojt-zaku", "web-01", "203.0.113.7")
	assert.Equal(t, "ojt-zaku", payload.ServerID)
	assert.Equal(t, "web-01", payload.Hostname)
	assert.Equal(t, "203.0.113.7", payload.ServerIP)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"server_id":"ojt-zaku"`)
	assert.Contains(t, string(raw), `"hostname":"web-01"`)
	assert.Contains(t, string(raw), `"server_ip":"203.0.113.7"`)
}

func TestMonthlyPayloadIncludesServerIDAndDiagnosticFields(t *testing.T) {
	data := MonthlyReportData{
		Month: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
	}
	payload := monthlyPayload(data, "ojt-zaku", "web-01", "203.0.113.7")
	assert.Equal(t, "ojt-zaku", payload.ServerID)
	assert.Equal(t, "web-01", payload.Hostname)
	assert.Equal(t, "203.0.113.7", payload.ServerIP)

	raw, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.Contains(t, string(raw), `"server_id":"ojt-zaku"`)
	assert.Contains(t, string(raw), `"hostname":"web-01"`)
	assert.Contains(t, string(raw), `"server_ip":"203.0.113.7"`)
}
