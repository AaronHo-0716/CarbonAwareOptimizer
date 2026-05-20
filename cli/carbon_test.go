package main

import (
	"testing"
	"time"
)

// UT-01 — TestCalculateImpact_CoreFormula
//
// Pins the scalar-intensity carbon formula in calculateImpact (carbon.go:278)
// against a single-resource fixture. Reference values are computed from the
// loaded coefficients, not hardcoded magic numbers — so the test fails only
// when the *formula* drifts, not when the CSV data is updated.
//
// Includes the per-instance networking overhead (carbon.go:351–355) which the
// spec's reference math omits.
func TestCalculateImpact_CoreFormula(t *testing.T) {
	mustChdirToRoot(t)
	embodied, vcpu, x86, arm := loadCoeffsAndMaps(t)
	plan := mustLoadFixturePlan(t, "ut01_single_m5_large")

	// Equal work/idle percentages collapse to a flat 30% utilization,
	// independent of the work-hour window. Keeps the closed form trivial.
	sched := UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 30.0, IdlePct: 30.0}
	const intensity = 400.0

	impacts, _, _ := calculateImpact(
		&plan, "us-east-1", intensity,
		embodied, vcpu, x86, arm,
		"medium", 0, sched,
	)

	if len(impacts) != 1 {
		t.Fatalf("expected 1 impact, got %d", len(impacts))
	}

	// Closed-form reference matching carbon.go:325–355 for aws_instance.
	const PUE = 1.135
	vcpus := float64(vcpu["m5.large"])
	utilization := 0.30
	avgW := x86.MinWatts + (x86.MaxWatts-x86.MinWatts)*utilization
	computeKwh := avgW * vcpus * 24.0 * PUE / 1000.0
	opsCompute := computeKwh * intensity / 1000.0
	// Network overhead (medium profile defaults to 100 GB/month).
	netKwh := (100.0 / 30.0) * 0.001 * PUE
	opsNet := netKwh * intensity / 1000.0
	expectedOps := opsCompute + opsNet

	approxEqual(t, impacts[0].DailyOps, expectedOps, 1.0, "DailyOps")

	expectedEmb := embodied["m5.large"] / 1460.0
	approxEqual(t, impacts[0].DailyEmb, expectedEmb, 1.0, "DailyEmb")
}

// UT-02 — TestCalculateImpact_ResourceCoverage
//
// Asserts that calculateImpact emits one ResourceImpact per supported resource
// type and that every entry carries plausible non-negative values. Guards
// against silent early-returns or dropped resource branches.
func TestCalculateImpact_ResourceCoverage(t *testing.T) {
	mustChdirToRoot(t)
	embodied, vcpu, x86, arm := loadCoeffsAndMaps(t)
	plan := mustLoadFixturePlan(t, "ut02_mixed_resources")

	sched := UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 50.0, IdlePct: 10.0}
	// ASG impact is computed against the hardcoded "t3.medium" instance type
	// per carbon.go:312 — the launch_template name in the fixture is not used
	// by the carbon engine (only by the pricing engine).
	impacts, _, _ := calculateImpact(
		&plan, "us-east-1", 250.0,
		embodied, vcpu, x86, arm,
		"medium", 1_000_000, sched,
	)

	if len(impacts) != 5 {
		t.Fatalf("expected 5 impacts, got %d (%+v)", len(impacts), impacts)
	}

	counts := map[string]int{}
	for _, imp := range impacts {
		if imp.DailyOps <= 0 {
			t.Errorf("%s/%s: DailyOps should be > 0, got %v", imp.Type, imp.Name, imp.DailyOps)
		}
		if imp.DailyEmb < 0 {
			t.Errorf("%s/%s: DailyEmb should be >= 0, got %v", imp.Type, imp.Name, imp.DailyEmb)
		}
		counts[imp.Type]++
	}

	wantTypes := []string{"aws_instance", "aws_autoscaling_group", "aws_ebs_volume", "aws_lambda_function", "aws_lb"}
	for _, ty := range wantTypes {
		if counts[ty] != 1 {
			t.Errorf("expected exactly 1 impact of type %s, got %d", ty, counts[ty])
		}
	}
}

// UT-03 — TestSimulateGraviton_TypeMappingAndCoefficients
//
// Pins the graviton rewrite table (carbon.go:779) and asserts directionally
// that Graviton rewrites reduce operational emissions vs. the baseline x86
// run. Also guards against accidental inversion of the x86 / ARM coefficient
// tables in the CSV.
func TestSimulateGraviton_TypeMappingAndCoefficients(t *testing.T) {
	mustChdirToRoot(t)
	embodied, vcpu, x86, arm := loadCoeffsAndMaps(t)

	// Sanity guard: ARM should consume less than x86 at peak. Catches the
	// "someone swapped the CSV rows" bug, which would silently invert the
	// Graviton claim in production.
	if arm.MaxWatts >= x86.MaxWatts {
		t.Fatalf("ARM MaxWatts (%.3f) should be < x86 MaxWatts (%.3f) — coefficient CSV may be inverted", arm.MaxWatts, x86.MaxWatts)
	}

	// simulateGraviton mutates the underlying slice elements (Values is a
	// reference type), so we need two independent loads of the plan.
	baseline := mustLoadFixturePlan(t, "ut03_graviton_candidates")
	gravTarget := mustLoadFixturePlan(t, "ut03_graviton_candidates")

	rewritten := simulateGraviton(gravTarget)

	wantTypes := []string{"m6g.large", "c7g.xlarge", "t4g.medium", "x99.unknown"}
	for i, want := range wantTypes {
		got, _ := rewritten.PlannedValues.RootModule.Resources[i].Values["instance_type"].(string)
		if got != want {
			t.Errorf("resource[%d]: got instance_type=%q, want %q", i, got, want)
		}
	}

	sched := UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 50.0, IdlePct: 10.0}
	const intensity = 400.0

	baseImpacts, _, _ := calculateImpact(&baseline, "us-east-1", intensity, embodied, vcpu, x86, arm, "medium", 0, sched)
	gravImpacts, _, _ := calculateImpact(&rewritten, "us-east-1", intensity, embodied, vcpu, x86, arm, "medium", 0, sched)

	// Sum DailyOps across the three rewritten instances (skip the synthetic
	// x99.unknown, whose impact is identical in both runs).
	rewrittenNames := map[string]bool{"m5": true, "c5": true, "t3": true}
	var baseSum, gravSum float64
	for _, imp := range baseImpacts {
		if rewrittenNames[imp.Name] {
			baseSum += imp.DailyOps
		}
	}
	for _, imp := range gravImpacts {
		if rewrittenNames[imp.Name] {
			gravSum += imp.DailyOps
		}
	}
	if !(gravSum < baseSum) {
		t.Errorf("graviton DailyOps sum (%.6f) should be strictly less than baseline (%.6f)", gravSum, baseSum)
	}
}

// UT-04 — TestCalculateImpactWithSeries_HourlyVariation
//
// Pins the time-series variant of the carbon formula (carbon.go:420) against
// a synthetic 24-hour intensity series. Verifies (a) the scalar daily total
// matches a closed-form reference and (b) the per-hour series sums back to
// the scalar within numerical tolerance.
func TestCalculateImpactWithSeries_HourlyVariation(t *testing.T) {
	mustChdirToRoot(t)
	embodied, vcpu, x86, arm := loadCoeffsAndMaps(t)
	plan := mustLoadFixturePlan(t, "ut01_single_m5_large")

	// Build a 24-point hourly series. Hours 9–16 (8 hours) carry 600 gCO2/kWh,
	// the other 16 hours carry 300 gCO2/kWh. utilizationAtTime treats the
	// work window as [WorkStartHour, WorkEndHour), so 17 is idle.
	series := make([]CarbonIntensityPoint, 24)
	for h := 0; h < 24; h++ {
		intensity := 300.0
		if h >= 9 && h <= 16 {
			intensity = 600.0
		}
		series[h] = CarbonIntensityPoint{
			Timestamp: time.Date(2026, 1, 1, h, 0, 0, 0, time.UTC),
			Intensity: intensity,
		}
	}

	sched := UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 60.0, IdlePct: 10.0}

	impacts, totalOps, _, opsSeries := calculateImpactWithSeries(
		&plan, "us-east-1", series,
		embodied, vcpu, x86, arm,
		"medium", 0, sched,
	)
	if len(impacts) != 1 {
		t.Fatalf("expected 1 impact, got %d", len(impacts))
	}
	if len(opsSeries) != 24 {
		t.Fatalf("expected 24 series points, got %d", len(opsSeries))
	}

	// Closed-form reference matching carbon.go:488–515 for aws_instance.
	const PUE = 1.135
	vcpus := float64(vcpu["m5.large"])
	wWork := x86.MinWatts + (x86.MaxWatts-x86.MinWatts)*0.60
	wIdle := x86.MinWatts + (x86.MaxWatts-x86.MinWatts)*0.10

	kwhPerHourCompWork := wWork * vcpus * 1.0 * PUE / 1000.0
	kwhPerHourCompIdle := wIdle * vcpus * 1.0 * PUE / 1000.0
	opsCompute := 8.0*kwhPerHourCompWork*600.0/1000.0 + 16.0*kwhPerHourCompIdle*300.0/1000.0

	// Network overhead per hour: (100 GB/month) / 30 days / 24 h * 0.001 kWh/GB * PUE.
	kwhPerHourNet := (100.0 / 30.0 / 24.0) * 0.001 * PUE
	opsNet := 8.0*kwhPerHourNet*600.0/1000.0 + 16.0*kwhPerHourNet*300.0/1000.0

	expected := opsCompute + opsNet
	approxEqual(t, totalOps, expected, 1.0, "scalar daily ops")

	// Per-hour series should sum to the scalar.
	var seriesSum float64
	for _, p := range opsSeries {
		seriesSum += p.Emissions
	}
	approxEqual(t, seriesSum, totalOps, 0.5, "sum of opsSeries == totalOps")
}
