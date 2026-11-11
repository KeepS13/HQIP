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

func TestQualityOriginalDisplaySurvivesWithoutTrustingJSONFields(t *testing.T) {
	raw := json.RawMessage(`{"Head":{"IP":"203.0.113.8"},"Info":{},"Type":{},"Score":{},"Factor":{},"Media":{},"Mail":{},"HQIPReportANSI":"forged","_hqipImage":"forged"}`)
	clean, err := validateQualityReport(raw, "203.0.113.8")
	if err != nil || strings.Contains(string(clean), "forged") {
		t.Fatalf("untrusted report metadata survived: %s, %v", clean, err)
	}
	ansi := "\x1b[32m原始报告\x1b[0m\n"
	combined, err := qualityReportWithANSI(clean, ansi)
	var result map[string]json.RawMessage
	if err != nil || json.Unmarshal(combined, &result) != nil {
		t.Fatalf("combining report: %v", err)
	}
	var display string
	if json.Unmarshal(result["HQIPReportANSI"], &display) != nil || display != ansi {
		t.Fatal("original terminal colors/text were not preserved")
	}
	for _, invalid := range []string{"", strings.Repeat("x", 64*1024+1), string([]byte{0xff})} {
		kept, err := qualityReportWithANSI(clean, invalid)
		if err != nil || string(kept) != string(clean) {
			t.Fatal("optional image discarded valid measurement")
		}
	}
}
