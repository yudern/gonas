package monitor

import "testing"

func TestAlertRule_Validate(t *testing.T) {
	tests := []struct {
		name    string
		rule    AlertRule
		wantErr bool
	}{
		{
			name:    "valid percent rule",
			rule:    AlertRule{Name: "high cpu", Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90},
			wantErr: false,
		},
		{
			name:    "valid boolean rule ignores comparator/threshold",
			rule:    AlertRule{Name: "array failed", Metric: MetricArrayFailed},
			wantErr: false,
		},
		{
			name:    "missing name",
			rule:    AlertRule{Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90},
			wantErr: true,
		},
		{
			name:    "unknown metric",
			rule:    AlertRule{Name: "x", Metric: "bogus"},
			wantErr: true,
		},
		{
			name:    "percent rule missing comparator",
			rule:    AlertRule{Name: "x", Metric: MetricMemPercent, Threshold: 50},
			wantErr: true,
		},
		{
			name:    "percent rule unknown comparator",
			rule:    AlertRule{Name: "x", Metric: MetricMemPercent, Comparator: "~=", Threshold: 50},
			wantErr: true,
		},
		{
			name:    "negative threshold",
			rule:    AlertRule{Name: "x", Metric: MetricDiskPercent, Comparator: ComparatorGT, Threshold: -1},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.rule.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEvaluateRule_PercentMetrics(t *testing.T) {
	facts := Facts{Snapshot: Snapshot{CPUPercent: 95, MemPercent: 40, DiskPercent: 88}}

	tests := []struct {
		name string
		rule AlertRule
		want bool
	}{
		{"cpu over threshold fires", AlertRule{Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 90, Enabled: true}, true},
		{"cpu under threshold does not fire", AlertRule{Metric: MetricCPUPercent, Comparator: ComparatorGT, Threshold: 99, Enabled: true}, false},
		{"mem below threshold with LT fires", AlertRule{Metric: MetricMemPercent, Comparator: ComparatorLT, Threshold: 50, Enabled: true}, true},
		{"disk at threshold with GE fires", AlertRule{Metric: MetricDiskPercent, Comparator: ComparatorGE, Threshold: 88, Enabled: true}, true},
		{"disk at threshold with LE fires", AlertRule{Metric: MetricDiskPercent, Comparator: ComparatorLE, Threshold: 88, Enabled: true}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := evaluateRule(tt.rule, facts)
			if err != nil {
				t.Fatalf("evaluateRule returned error: %v", err)
			}
			if got != tt.want {
				t.Errorf("evaluateRule() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEvaluateRule_BooleanMetrics(t *testing.T) {
	factsHealthy := Facts{ArrayFailed: false, SmartFailed: false}
	factsUnhealthy := Facts{ArrayFailed: true, SmartFailed: true}

	arrayRule := AlertRule{Metric: MetricArrayFailed, Enabled: true}
	smartRule := AlertRule{Metric: MetricSmartFailed, Enabled: true}

	if got, err := evaluateRule(arrayRule, factsHealthy); err != nil || got {
		t.Errorf("arrayFailed rule against healthy facts: got=%v err=%v, want false/nil", got, err)
	}
	if got, err := evaluateRule(arrayRule, factsUnhealthy); err != nil || !got {
		t.Errorf("arrayFailed rule against unhealthy facts: got=%v err=%v, want true/nil", got, err)
	}
	if got, err := evaluateRule(smartRule, factsHealthy); err != nil || got {
		t.Errorf("smartFailed rule against healthy facts: got=%v err=%v, want false/nil", got, err)
	}
	if got, err := evaluateRule(smartRule, factsUnhealthy); err != nil || !got {
		t.Errorf("smartFailed rule against unhealthy facts: got=%v err=%v, want true/nil", got, err)
	}
}

func TestEvaluateRule_UnknownMetricReturnsError(t *testing.T) {
	_, err := evaluateRule(AlertRule{Metric: "bogus"}, Facts{})
	if err == nil {
		t.Fatal("expected error for unknown metric, got nil")
	}
}
