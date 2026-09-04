package storage

import "testing"

const sampleSmartctlOutput = `smartctl 7.3 2022-02-28 r5338 [x86_64-linux-6.1.0] (local build)
Copyright (C) 2002-22, Bruce Allen, Christian Franke, www.smartmontools.org

=== START OF INFORMATION SECTION ===
Model Family:     Western Digital Red
Device Model:     WDC WD40EFAX-68JH4N1
Serial Number:    WD-ABC123

=== START OF READ SMART DATA SECTION ===
SMART overall-health self-assessment test result: PASSED

ID# ATTRIBUTE_NAME          FLAG     VALUE WORST THRESH TYPE      UPDATED  WHEN_FAILED RAW_VALUE
  9 Power_On_Hours          0x0032   098   098   000    Old_age   Always       -       9421
194 Temperature_Celsius     0x0022   116   105   000    Old_age   Always       -       31
`

const sampleSmartctlFailedOutput = `SMART overall-health self-assessment test result: FAILED!`

func TestParseSmartOutput_Passed(t *testing.T) {
	h := parseSmartOutput([]byte(sampleSmartctlOutput))

	if !h.Passed {
		t.Error("expected Passed=true for a PASSED result")
	}
	if h.TempCelsius == nil {
		t.Fatal("expected TempCelsius to be parsed, got nil")
	}
	if *h.TempCelsius != 31 {
		t.Errorf("expected temp 31, got %d", *h.TempCelsius)
	}
}

func TestParseSmartOutput_Failed(t *testing.T) {
	h := parseSmartOutput([]byte(sampleSmartctlFailedOutput))
	if h.Passed {
		t.Error("expected Passed=false for a FAILED result")
	}
}

func TestParseSmartOutput_NoTempAttribute(t *testing.T) {
	h := parseSmartOutput([]byte("SMART overall-health self-assessment test result: PASSED\n"))
	if h.TempCelsius != nil {
		t.Errorf("expected nil TempCelsius when attribute table absent, got %v", *h.TempCelsius)
	}
}
