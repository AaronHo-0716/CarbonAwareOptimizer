package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRedactPlanBytesAgainstRealPlan exercises the redactor against the
// repo-checked-in plan.json and verifies that every known secret no longer
// appears in the redacted output.
func TestRedactPlanBytesAgainstRealPlan(t *testing.T) {
	wd, _ := os.Getwd()
	planPath := filepath.Join(wd, "..", "plan.json")
	raw, err := os.ReadFile(planPath)
	if err != nil {
		t.Skipf("plan.json not available at %s: %v", planPath, err)
	}

	redacted, err := redactPlanBytes(raw)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	// 1. Known secrets must be gone.
	mustBeAbsent := []string{
		"FYP_SUCKS!",
		"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",
		"mcdonalds",
		"debugging",
	}
	for _, s := range mustBeAbsent {
		if strings.Contains(string(redacted), s) {
			t.Errorf("redacted output still contains secret %q", s)
		}
	}

	// 2. prior_state must be removed entirely.
	var root map[string]any
	if err := json.Unmarshal(redacted, &root); err != nil {
		t.Fatalf("re-parse redacted: %v", err)
	}
	if _, ok := root["prior_state"]; ok {
		t.Error("prior_state was not stripped from the redacted plan")
	}

	// 3. variables.*.value should all be the redaction marker.
	if vars, ok := root["variables"].(map[string]any); ok {
		for k, v := range vars {
			entry, ok := v.(map[string]any)
			if !ok {
				continue
			}
			if val, ok := entry["value"]; ok {
				if val != redactedMarker {
					t.Errorf("variable %q was not redacted (got %#v)", k, val)
				}
			}
		}
	}

	// 4. Resource type / instance-size fields must remain readable so the
	//    analysis can still parse the document.
	if !strings.Contains(string(redacted), "aws_instance") {
		t.Error("expected aws_instance type to remain in redacted output")
	}
}
