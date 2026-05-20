package main

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// projectRoot walks up from the current working directory until it finds a
// go.mod, which marks the repository root. Tests run from cli/, so this
// typically walks up one level.
func projectRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod from %q", dir)
		}
		dir = parent
	}
}

// mustChdirToRoot temporarily chdirs to the project root so the CSV loaders
// (parseEmbodiedEmissions, parseInstanceVCPUs, getUseCoefficients) find their
// data files via the relative paths they expect. t.Chdir restores the original
// directory automatically when the test ends.
func mustChdirToRoot(t *testing.T) {
	t.Helper()
	t.Chdir(projectRoot(t))
}

// mustLoadFixturePlan reads cli/testdata/<name>.json and unmarshals into a
// TFPlan. Call after mustChdirToRoot so the path resolves correctly.
func mustLoadFixturePlan(t *testing.T, name string) TFPlan {
	t.Helper()
	path := filepath.Join("cli", "testdata", name+".json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	var plan TFPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		t.Fatalf("parse fixture %s: %v", path, err)
	}
	return plan
}

// approxEqual fails the test if |got-want|/|want| exceeds tolerancePct/100.
// When want is zero, falls back to an absolute tolerance of tolerancePct/100.
func approxEqual(t *testing.T, got, want, tolerancePct float64, label string) {
	t.Helper()
	tol := tolerancePct / 100.0
	var rel float64
	if want == 0 {
		rel = math.Abs(got)
	} else {
		rel = math.Abs(got-want) / math.Abs(want)
	}
	if rel > tol {
		t.Errorf("%s: got %.6f, want %.6f (tolerance %.2f%%, relative diff %.4f%%)",
			label, got, want, tolerancePct, rel*100)
	}
}

// loadCoeffsAndMaps reads all three CSVs that the carbon calculation depends
// on, using the real production loaders. Must be called after mustChdirToRoot.
func loadCoeffsAndMaps(t *testing.T) (embodied map[string]float64, vcpu map[string]int, x86, arm UseCoeff) {
	t.Helper()
	embodied = parseEmbodiedEmissions()
	vcpu = parseInstanceVCPUs()
	x86, arm = getUseCoefficients()
	if len(embodied) == 0 || len(vcpu) == 0 {
		t.Fatalf("coefficient CSVs not loaded — are you running from project root? embodied=%d vcpu=%d", len(embodied), len(vcpu))
	}
	return
}
