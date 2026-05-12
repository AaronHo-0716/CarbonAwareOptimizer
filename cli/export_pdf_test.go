package main

import (
	"os"
	"testing"
	"time"
)

// TestBuildReportPDF_Smoke is a black-box smoke test: build a representative
// model, render to /tmp, confirm the file exists and starts with the PDF
// magic bytes. It does not validate visual layout.
func TestBuildReportPDF_Smoke(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	ops := make([]OpsEmissionPoint, 0, 24)
	sim := make([]OpsEmissionPoint, 0, 24)
	for i := 0; i < 24; i++ {
		ops = append(ops, OpsEmissionPoint{Timestamp: now.Add(time.Duration(i) * time.Hour), Emissions: 0.10 + 0.02*float64(i%6)})
		sim = append(sim, OpsEmissionPoint{Timestamp: now.Add(time.Duration(i) * time.Hour), Emissions: 0.07 + 0.015*float64(i%6)})
	}

	m := model{
		selectedPath:  "/tmp/sample-plan.json",
		region:        "ap-southeast-5",
		gridIntensity: 580.5,
		utilization:   UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 75, IdlePct: 1},
		impacts: []ResourceImpact{
			{Type: "aws_instance", Name: "backend", Instance: "m5.large", Count: 3, DailyOps: 0.42, DailyEmb: 0.18, TotalDaily: 0.60},
			{Type: "aws_db_instance", Name: "primary", Instance: "db.t3.medium", Count: 1, DailyOps: 0.15, DailyEmb: 0.05, TotalDaily: 0.20},
		},
		totalOps:     0.57,
		totalEmb:     0.23,
		opsSeries:    ops,
		simOpsSeries: sim,
		simDone:      true,
		simRegion:    "eu-west-1",
		simImpacts: []ResourceImpact{
			{Type: "aws_instance", Name: "backend", Instance: "m6g.large", Count: 3, DailyOps: 0.30, DailyEmb: 0.15, TotalDaily: 0.45},
			{Type: "aws_db_instance", Name: "primary", Instance: "db.t4g.medium", Count: 1, DailyOps: 0.11, DailyEmb: 0.04, TotalDaily: 0.15},
		},
		simOps:   0.41,
		simEmb:   0.19,
		simTotal: 0.60,
		matrixData: []MatrixRow{
			{RegionName: "eu-west-1 (Ireland)", Intensity: 220.0, Total: 0.30, DeltaStr: "-25.0%", HourlyCost: 0.0832, MonthlyCost: 60.05, CostKnown: true},
			{RegionName: "ap-southeast-5 (Malaysia) ★", Intensity: 580.5, Total: 0.60, DeltaStr: "—", HourlyCost: 0.0901, MonthlyCost: 65.84, CostKnown: true},
		},
		baselineCost: CostSummary{
			Region: "ap-southeast-5",
			Resources: []CostResource{
				{Address: "aws_instance.backend", Type: "aws_instance", Name: "backend", Hourly: 0.080, Monthly: 58.4, Available: true},
			},
			TotalHourly:  0.0901,
			TotalMonthly: 65.84,
		},
		targetCost: CostSummary{
			Region: "eu-west-1",
			Resources: []CostResource{
				{Address: "aws_instance.backend", Type: "aws_instance", Name: "backend", Hourly: 0.075, Monthly: 54.75, Available: true},
			},
			TotalHourly:  0.0832,
			TotalMonthly: 60.05,
		},
		aiContentRaw: "## Recommendations\n\n1. Migrate to m6g.large — minor application changes for arm64.\n2. Move to eu-west-1 for lower grid intensity.\n3. Enable AWS Instance Scheduler off-hours.\n",
		optType:      "graviton",
	}

	tmp, err := os.CreateTemp("", "carbon-report-*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	if os.Getenv("KEEP_PDF") == "" {
		defer os.Remove(tmp.Name())
	}

	if err := buildReportPDF(m, tmp.Name()); err != nil {
		t.Fatalf("buildReportPDF failed: %v", err)
	}

	info, err := os.Stat(tmp.Name())
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() < 1024 {
		t.Fatalf("PDF unexpectedly small: %d bytes", info.Size())
	}

	head, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	if string(head[:4]) != "%PDF" {
		t.Fatalf("output does not start with %%PDF magic: %q", string(head[:8]))
	}
	t.Logf("wrote %d-byte PDF to %s", info.Size(), tmp.Name())
}
