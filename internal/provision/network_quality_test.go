package provision

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestQualityReportRejectsWrongExitAndMalformedOutput(t *testing.T) {
	valid := `{"Head":{"IP":"203.0.113.8"},"Info":{},"Type":{},"Score":{},"Factor":{},"Media":{},"Mail":{}}`
	if _, err := validateQualityReport(json.RawMessage(valid), "203.0.113.8"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{valid, `null`, `[]`, `{"Head":{"IP":"203.0.113.9"}}`, strings.Repeat("a", 200*1024)} {
		if _, err := validateQualityReport(json.RawMessage(raw), "203.0.113.9"); err == nil {
			t.Fatal("invalid or different exit accepted")
		}
	}
}
