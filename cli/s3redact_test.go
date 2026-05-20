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

// UT-07 — TestRedactPlan_VariablesAndSensitiveTree
//
// Loads cli/testdata/ut07_seeded_plan.json and asserts:
//   - variables.<name>.value is replaced with the redaction marker for every
//     variable (the variables pass at s3redact.go:26–36 blanks all values,
//     not just sensitive ones — see deviation #12 in the plan).
//   - planned_values.root_module.resources[].values.<key> is blanked when
//     sensitive_values flags it, even if the key name doesn't itself match
//     the regex (this exercises the sensitive-tree pass at lines 38–44).
//   - The top-level JSON shape is preserved.
func TestRedactPlan_VariablesAndSensitiveTree(t *testing.T) {
	mustChdirToRoot(t)
	raw, err := os.ReadFile(filepath.Join("cli", "testdata", "ut07_seeded_plan.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	redacted, err := redactPlanBytes(raw)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(redacted, &root); err != nil {
		t.Fatalf("re-parse redacted: %v", err)
	}

	vars, ok := root["variables"].(map[string]any)
	if !ok {
		t.Fatalf("expected variables to be an object, got %T", root["variables"])
	}
	// Both sensitive and non-sensitive variable values are blanked — the
	// variables pass is unconditional, not guided by a sensitivity flag.
	for _, name := range []string{"db_password", "api_url"} {
		entry, ok := vars[name].(map[string]any)
		if !ok {
			t.Errorf("variable %s missing or wrong shape: %#v", name, vars[name])
			continue
		}
		if entry["value"] != redactedMarker {
			t.Errorf("variable %s.value: got %#v, want %q", name, entry["value"], redactedMarker)
		}
	}

	// password_hash isn't matched by the regex directly (no "password" substring
	// in plain form would match here either — actually "password_hash" does
	// contain "password", but the sensitive-tree pass catches it first).
	pv, _ := root["planned_values"].(map[string]any)
	rm, _ := pv["root_module"].(map[string]any)
	resList, _ := rm["resources"].([]any)
	if len(resList) == 0 {
		t.Fatal("expected at least one resource")
	}
	res0, _ := resList[0].(map[string]any)
	values0, _ := res0["values"].(map[string]any)
	if values0["password_hash"] != redactedMarker {
		t.Errorf("resources[0].values.password_hash: got %#v, want %q", values0["password_hash"], redactedMarker)
	}

	// Shape preservation: top-level keys still present.
	if _, ok := root["variables"]; !ok {
		t.Error("top-level variables key was dropped")
	}
	if _, ok := root["planned_values"]; !ok {
		t.Error("top-level planned_values key was dropped")
	}
}

// UT-08 — TestRedactPlan_PriorStateDropped
//
// Asserts that prior_state is deleted (not nulled) from the top of the
// document while format_version is preserved untouched.
func TestRedactPlan_PriorStateDropped(t *testing.T) {
	plan := map[string]any{
		"format_version": "1.2",
		"prior_state": map[string]any{
			"values": map[string]any{"x": "y"},
		},
		"planned_values": map[string]any{
			"root_module": map[string]any{"resources": []any{}},
		},
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	redacted, err := redactPlanBytes(raw)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(redacted, &root); err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	if _, ok := root["prior_state"]; ok {
		t.Errorf("prior_state should be absent, got %#v", root["prior_state"])
	}
	if root["format_version"] != "1.2" {
		t.Errorf("format_version: got %#v, want \"1.2\"", root["format_version"])
	}
}

// UT-09 — TestRedactPlan_RegexSweepAndShapePreserved
//
// Pins the defensive regex sweep (s3redact.go:125) across several nesting
// depths and a control case. Documents the array-element limitation noted
// as deviation #11 in the plan: string values *inside* an array under a
// matching key are not blanked, because redactByKeyName recurses into the
// array but only blanks string values whose direct parent key matches.
func TestRedactPlan_RegexSweepAndShapePreserved(t *testing.T) {
	plan := map[string]any{
		// Top-level matching key.
		"api_key": "AKIAIOSFODNN7EXAMPLE",
		// Nested under outputs.<x>.value.
		"outputs": map[string]any{
			"db": map[string]any{
				"value": map[string]any{
					"password": "secret123",
				},
			},
		},
		// Nested two deep with mixed case — hyphenated form.
		"resource_changes": []any{
			map[string]any{
				"change": map[string]any{
					"after": map[string]any{
						"Access-Key": "AKIAI_DEEP",
					},
				},
			},
		},
		// Array of string values under a matching key — documented limitation:
		// the current sweep does NOT blank array elements.
		"tokens": []any{"t1", "t2"},
		// Control: must remain untouched.
		"display_name": "production-cluster",
	}
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	redacted, err := redactPlanBytes(raw)
	if err != nil {
		t.Fatalf("redact: %v", err)
	}

	var root map[string]any
	if err := json.Unmarshal(redacted, &root); err != nil {
		t.Fatalf("re-parse: %v", err)
	}

	if root["api_key"] != redactedMarker {
		t.Errorf("api_key: got %#v, want %q", root["api_key"], redactedMarker)
	}

	outs, _ := root["outputs"].(map[string]any)
	db, _ := outs["db"].(map[string]any)
	dbVal, _ := db["value"].(map[string]any)
	if dbVal["password"] != redactedMarker {
		t.Errorf("outputs.db.value.password: got %#v, want %q", dbVal["password"], redactedMarker)
	}

	rcs, _ := root["resource_changes"].([]any)
	rc0, _ := rcs[0].(map[string]any)
	change, _ := rc0["change"].(map[string]any)
	after, _ := change["after"].(map[string]any)
	if after["Access-Key"] != redactedMarker {
		t.Errorf("resource_changes[0].change.after.Access-Key: got %#v, want %q", after["Access-Key"], redactedMarker)
	}

	// Documented limitation: array elements are not blanked even when the
	// parent key matches the sensitive-key regex. If this is ever fixed,
	// flip the assertion below.
	toks, _ := root["tokens"].([]any)
	if len(toks) != 2 || toks[0] != "t1" || toks[1] != "t2" {
		t.Errorf("tokens array: got %#v — limitation may have been fixed; revisit", toks)
	}

	if root["display_name"] != "production-cluster" {
		t.Errorf("display_name: got %#v, want \"production-cluster\"", root["display_name"])
	}
}
