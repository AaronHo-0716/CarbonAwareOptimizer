package main

import (
	"encoding/json"
	"regexp"
	"strings"
)

const redactedMarker = "<REDACTED>"

// sensitiveKeyRe catches keys that almost certainly hold secrets even when
// Terraform itself didn't flag them as sensitive (defence in depth).
var sensitiveKeyRe = regexp.MustCompile(`(?i)(password|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)`)

// redactPlanBytes walks the raw Terraform plan JSON and replaces every value
// that may carry a secret with the string "<REDACTED>". The shape of the
// document is preserved so it can still be parsed downstream for analysis or
// re-simulation. It is intentionally permissive: unknown top-level keys are
// passed through untouched.
func redactPlanBytes(raw []byte) ([]byte, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}

	// 1. tfvars: redact every variable value.
	if vars, ok := root["variables"].(map[string]any); ok {
		for k, v := range vars {
			if entry, ok := v.(map[string]any); ok {
				if _, ok := entry["value"]; ok {
					entry["value"] = redactedMarker
					vars[k] = entry
				}
			}
		}
	}

	// 2. planned_values: walk root_module + child_modules and redact
	//    values flagged by the parallel sensitive_values shape.
	if pv, ok := root["planned_values"].(map[string]any); ok {
		if rm, ok := pv["root_module"].(map[string]any); ok {
			redactModule(rm)
		}
	}

	// 3. resource_changes: same logic applied to change.before/after.
	if rcs, ok := root["resource_changes"].([]any); ok {
		for _, rc := range rcs {
			rcMap, ok := rc.(map[string]any)
			if !ok {
				continue
			}
			change, ok := rcMap["change"].(map[string]any)
			if !ok {
				continue
			}
			redactPaired(change, "before", "before_sensitive")
			redactPaired(change, "after", "after_sensitive")
		}
	}

	// 4. prior_state often contains the previous apply's full values, which
	//    may include secrets that aren't flagged sensitive any more. Drop it.
	delete(root, "prior_state")

	// 5. Defensive sweep over what's left — catches anything we missed.
	redactByKeyName(root)

	return json.MarshalIndent(root, "", "  ")
}

func redactModule(mod map[string]any) {
	if resources, ok := mod["resources"].([]any); ok {
		for _, r := range resources {
			res, ok := r.(map[string]any)
			if !ok {
				continue
			}
			redactPaired(res, "values", "sensitive_values")
		}
	}
	if children, ok := mod["child_modules"].([]any); ok {
		for _, c := range children {
			if cm, ok := c.(map[string]any); ok {
				redactModule(cm)
			}
		}
	}
}

// redactPaired redacts entries in obj[valuesKey] for every key flagged in
// obj[sensitiveKey] as "truthy". The Terraform plan format uses a parallel
// structure where sensitive flags mirror the values tree.
func redactPaired(obj map[string]any, valuesKey, sensitiveKey string) {
	values, ok := obj[valuesKey].(map[string]any)
	if !ok {
		return
	}
	flags, _ := obj[sensitiveKey].(map[string]any)
	for k := range values {
		if flags != nil && isSensitiveFlag(flags[k]) {
			values[k] = redactedMarker
		}
	}
}

func isSensitiveFlag(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case map[string]any:
		// Nested non-empty objects mean at least some leaf is sensitive;
		// at the top level we don't try to redact partial sub-trees,
		// we just blank the whole value.
		return len(t) > 0
	case []any:
		return len(t) > 0
	default:
		return false
	}
}

// redactByKeyName recursively replaces string values whose key matches a known
// secret pattern. Skips fields already redacted.
func redactByKeyName(node any) {
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if s, ok := v.(string); ok && s != redactedMarker && sensitiveKeyRe.MatchString(k) && strings.TrimSpace(s) != "" {
				n[k] = redactedMarker
				continue
			}
			redactByKeyName(v)
		}
	case []any:
		for _, v := range n {
			redactByKeyName(v)
		}
	}
}
