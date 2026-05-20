package main

import (
	"bytes"
	"os"
	"os/exec"
	"strings"
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

// UT-10 — TestExportPDF_ContentSections
//
// Stronger than the smoke test: builds a model with deliberately-recognisable
// resource names, generates a PDF, then uses pdftotext (when available) to
// extract the text layer and assert the report actually contains the impact
// table heading, the regional-matrix heading, and the unique resource ID.
// This catches "PDF magic bytes present but content sections missing"
// regressions that the smoke test alone would miss.
//
// Expected strings are pinned to the actual headings in cli/export_pdf.go
// ("Resource Carbon Impact" at line 156, "Regional Trade-off Matrix" at
// line 410). If those headings are renamed, this test will surface it.
func TestExportPDF_ContentSections(t *testing.T) {
	pdftotext, err := exec.LookPath("pdftotext")
	if err != nil {
		t.Skip("pdftotext not available on PATH — skipping text-layer assertions")
	}

	now := time.Now().UTC().Truncate(time.Hour)
	ops := make([]OpsEmissionPoint, 0, 24)
	for i := 0; i < 24; i++ {
		ops = append(ops, OpsEmissionPoint{
			Timestamp: now.Add(time.Duration(i) * time.Hour),
			Emissions: 0.05 + 0.01*float64(i%5),
		})
	}

	// Deliberately-unique resource names that should appear verbatim in the
	// PDF impact table (cli/export_pdf.go:165 writes imp.Name into the row).
	// Kept short — truncatePDF (export_pdf.go:649) caps Name-column cells at
	// roughly widthMm/1.8 ≈ 25 chars before appending "..." which would break
	// substring matching on the extracted text.
	uniqueWebID := "web_uniq_a1b2c3"
	uniqueVolID := "vol_uniq_x9y8z7"

	m := model{
		selectedPath:  "/tmp/sample-plan.json",
		region:        "us-east-1",
		gridIntensity: 400.0,
		utilization:   UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 50, IdlePct: 10},
		impacts: []ResourceImpact{
			{Type: "aws_instance", Name: uniqueWebID, Instance: "m5.large", Count: 1, DailyOps: 0.10, DailyEmb: 0.05, TotalDaily: 0.15},
			{Type: "aws_ebs_volume", Name: uniqueVolID, Instance: "Storage", Count: 1, DailyOps: 0.02, DailyEmb: 0.01, TotalDaily: 0.03},
		},
		totalOps:  0.12,
		totalEmb:  0.06,
		opsSeries: ops,
		matrixData: []MatrixRow{
			{RegionName: "us-east-1 (N. Virginia) ★", Intensity: 400, Total: 0.18, DeltaStr: "—", HourlyCost: 0.10, MonthlyCost: 73.00, CostKnown: true},
			{RegionName: "us-west-2 (Oregon)", Intensity: 180, Total: 0.10, DeltaStr: "-44.4%", HourlyCost: 0.10, MonthlyCost: 73.00, CostKnown: true},
			{RegionName: "ca-central-1 (Canada Central)", Intensity: 60, Total: 0.05, DeltaStr: "-72.2%", HourlyCost: 0.10, MonthlyCost: 73.00, CostKnown: true},
		},
		aiContentRaw: "## Recommendations\n\n1. Consider Graviton.\n",
	}

	tmp, err := os.CreateTemp("", "carbon-report-sections-*.pdf")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	defer os.Remove(tmp.Name())

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
	if string(head[:5]) != "%PDF-" {
		t.Fatalf("PDF magic missing: %q", string(head[:8]))
	}

	// Extract text layer with pdftotext.
	var out bytes.Buffer
	cmd := exec.Command(pdftotext, tmp.Name(), "-")
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("pdftotext: %v", err)
	}
	text := out.String()

	// Pinned to the real headings in cli/export_pdf.go. If these are renamed,
	// this assertion is the breakage signal.
	wantSubstrings := []string{
		"Resource Carbon Impact",   // pdfImpactTable heading, export_pdf.go:156
		"Regional Trade-off Matrix", // pdfMatrixTable heading, export_pdf.go:410
		uniqueWebID,                 // confirms imp.Name is written into the impact row
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(text, want) {
			t.Errorf("extracted PDF text missing %q", want)
		}
	}
}
