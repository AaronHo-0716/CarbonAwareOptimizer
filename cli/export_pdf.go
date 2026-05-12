package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/jung-kurt/gofpdf"
)

// pdfExportMsg is delivered to the Update loop when a PDF export finishes.
type pdfExportMsg struct {
	path string
	err  error
}

// exportPDFCmd writes the current model's analysis to a timestamped PDF in
// the working directory and returns a pdfExportMsg.
func (m model) exportPDFCmd() tea.Cmd {
	return func() tea.Msg {
		ts := time.Now().Format("20060102-150405")
		region := m.region
		if region == "" {
			region = "unknown"
		}
		fname := fmt.Sprintf("carbon-report-%s-%s.pdf", sanitizeFile(region), ts)
		cwd, err := os.Getwd()
		if err != nil {
			return pdfExportMsg{err: err}
		}
		path := filepath.Join(cwd, fname)
		if err := buildReportPDF(m, path); err != nil {
			return pdfExportMsg{err: err}
		}
		return pdfExportMsg{path: path}
	}
}

func sanitizeFile(s string) string {
	r := strings.NewReplacer("/", "-", " ", "-", string(os.PathSeparator), "-")
	return r.Replace(s)
}

// ── PDF builder ───────────────────────────────────────────────────────────────

const (
	pdfMarginL = 12.0
	pdfMarginR = 12.0
	pdfMarginT = 14.0
)

func buildReportPDF(m model, outPath string) error {
	data, err := buildReportPDFBytes(m)
	if err != nil {
		return err
	}
	return os.WriteFile(outPath, data, 0644)
}

// buildReportPDFBytes renders the same report as buildReportPDF but returns
// the bytes instead of writing them to disk. Used by the S3 uploader.
func buildReportPDFBytes(m model) ([]byte, error) {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(pdfMarginL, pdfMarginT, pdfMarginR)
	pdf.SetAutoPageBreak(true, 14)
	pdf.SetFont("Helvetica", "", 10)

	pdf.AddPage()
	pdfHeader(pdf, m)
	pdfSchedule(pdf, m)
	pdfImpactTable(pdf, m)
	if m.simDone && len(m.simImpacts) > 0 {
		pdfSideBySideTable(pdf, m)
	}
	pdfOpsChart(pdf, m)
	pdfMatrixTable(pdf, m)
	pdfCostTables(pdf, m)
	pdfAISection(pdf, m)

	var buf bytes.Buffer
	if err := pdf.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ── Section: header ───────────────────────────────────────────────────────────

func pdfHeader(pdf *gofpdf.Fpdf, m model) {
	pdf.SetFont("Helvetica", "B", 18)
	pdf.SetTextColor(34, 139, 34)
	pdf.Cell(0, 9, "Carbon-Aware Optimizer Report")
	pdf.Ln(10)

	pdf.SetFont("Helvetica", "", 9)
	pdf.SetTextColor(80, 80, 80)
	planLabel := m.selectedPath
	if planLabel == "" {
		planLabel = "(in-memory plan)"
	}
	pdf.Cell(0, 5, pdfText(fmt.Sprintf("Plan:        %s", planLabel)))
	pdf.Ln(5)
	pdf.Cell(0, 5, pdfText(fmt.Sprintf("Region:      %s", nonEmpty(m.region, "unknown"))))
	pdf.Ln(5)
	pdf.Cell(0, 5, pdfText(fmt.Sprintf("Generated:   %s", time.Now().Format(time.RFC1123))))
	pdf.Ln(5)
	if m.simDone {
		opt := m.optType
		if opt == "" {
			opt = "applied"
		}
		if opt == "region" && m.simRegion != "" {
			opt = fmt.Sprintf("Move to %s", m.simRegion)
		} else if opt == "graviton" {
			opt = "Migrate to Graviton"
		}
		pdf.Cell(0, 5, pdfText(fmt.Sprintf("Simulation:  %s", opt)))
		pdf.Ln(5)
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(3)
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// ── Section: utilization schedule ─────────────────────────────────────────────

func pdfSchedule(pdf *gofpdf.Fpdf, m model) {
	pdfSectionHeading(pdf, "Utilization Schedule")
	schedule := normalizeSchedule(m.utilization)
	workHours := scheduleWorkHours(schedule.WorkStartHour, schedule.WorkEndHour)
	idleHours := 24 - workHours
	pdf.SetFont("Helvetica", "", 10)
	pdf.MultiCell(0, 5, pdfText(fmt.Sprintf(
		"Every day: %02d:00-%02d:00 work (%d h @ %.0f%%), idle (%d h @ %.0f%%).",
		schedule.WorkStartHour, schedule.WorkEndHour, workHours, schedule.WorkPct,
		idleHours, schedule.IdlePct,
	)), "", "", false)
	pdf.Ln(3)
}

// ── Section: resource carbon impact ───────────────────────────────────────────

func pdfImpactTable(pdf *gofpdf.Fpdf, m model) {
	pdfSectionHeading(pdf, "Resource Carbon Impact")

	headers := []string{"Resource Type", "Name", "Instance", "Qty", "Ops kg", "Emb kg", "Total kg"}
	widths := []float64{30, 46, 24, 12, 20, 20, 22} // = 174mm, leaves room within 186mm printable
	aligns := []string{"L", "L", "L", "R", "R", "R", "R"}

	rows := make([][]string, 0, len(m.impacts)+1)
	for _, imp := range m.impacts {
		rows = append(rows, []string{
			imp.Type, imp.Name, imp.Instance,
			fmt.Sprintf("%d", imp.Count),
			fmt.Sprintf("%.4f", imp.DailyOps),
			fmt.Sprintf("%.4f", imp.DailyEmb),
			fmt.Sprintf("%.4f", imp.TotalDaily),
		})
	}
	rows = append(rows, []string{
		"-- TOTALS --", "", "", "",
		fmt.Sprintf("%.4f", m.totalOps),
		fmt.Sprintf("%.4f", m.totalEmb),
		fmt.Sprintf("%.4f", m.totalOps+m.totalEmb),
	})

	pdfTable(pdf, headers, widths, aligns, rows, len(rows)-1)
	pdf.Ln(4)
}

// ── Section: baseline vs sim side-by-side (landscape page) ────────────────────

func pdfSideBySideTable(pdf *gofpdf.Fpdf, m model) {
	// gofpdf's AddPageFormat swaps dimensions when orientation is "L", so pass
	// portrait-base A4 (210 x 297) here to get an actual landscape page.
	pdf.AddPageFormat("L", gofpdf.SizeType{Wd: 210, Ht: 297})
	pdfSectionHeading(pdf, fmt.Sprintf("Baseline vs Optimized (%s)", nonEmpty(m.simRegion, m.region)))

	headers := []string{"Type", "Name", "Instance", "Qty", "Ops", "Sim Ops", "Emb", "Sim Emb", "Total", "Sim Tot", "Δ %"}
	widths := []float64{32, 44, 22, 12, 20, 20, 20, 20, 22, 22, 18} // = 252mm in landscape (273 printable)
	aligns := []string{"L", "L", "L", "R", "R", "R", "R", "R", "R", "R", "R"}

	simByKey := make(map[string]ResourceImpact, len(m.simImpacts))
	for _, s := range m.simImpacts {
		simByKey[s.Type+"\x00"+s.Name] = s
	}
	fmtPct := func(base, sim float64) string {
		if base == 0 {
			return "—"
		}
		p := (sim - base) / base * 100
		if p >= 0 {
			return fmt.Sprintf("+%.1f%%", p)
		}
		return fmt.Sprintf("%.1f%%", p)
	}

	rows := make([][]string, 0, len(m.impacts)+1)
	for _, imp := range m.impacts {
		sim, ok := simByKey[imp.Type+"\x00"+imp.Name]
		simOps, simEmb, simTot, pct := "—", "—", "—", "—"
		if ok {
			simOps = fmt.Sprintf("%.4f", sim.DailyOps)
			simEmb = fmt.Sprintf("%.4f", sim.DailyEmb)
			simTot = fmt.Sprintf("%.4f", sim.TotalDaily)
			pct = fmtPct(imp.TotalDaily, sim.TotalDaily)
		}
		rows = append(rows, []string{
			imp.Type, imp.Name, imp.Instance,
			fmt.Sprintf("%d", imp.Count),
			fmt.Sprintf("%.4f", imp.DailyOps), simOps,
			fmt.Sprintf("%.4f", imp.DailyEmb), simEmb,
			fmt.Sprintf("%.4f", imp.TotalDaily), simTot, pct,
		})
	}
	baseTotal := m.totalOps + m.totalEmb
	rows = append(rows, []string{
		"-- TOTALS --", "", "", "",
		fmt.Sprintf("%.4f", m.totalOps),
		fmt.Sprintf("%.4f", m.simOps),
		fmt.Sprintf("%.4f", m.totalEmb),
		fmt.Sprintf("%.4f", m.simEmb),
		fmt.Sprintf("%.4f", baseTotal),
		fmt.Sprintf("%.4f", m.simTotal),
		fmtPct(baseTotal, m.simTotal),
	})

	pdfTable(pdf, headers, widths, aligns, rows, len(rows)-1)

	// Savings summary line.
	pdf.Ln(3)
	savings := baseTotal - m.simTotal
	pct := 0.0
	if baseTotal > 0 {
		pct = (savings / baseTotal) * 100
	}
	pdf.SetFont("Helvetica", "B", 11)
	if savings >= 0 {
		pdf.SetTextColor(34, 139, 34)
		pdf.Cell(0, 6, pdfText(fmt.Sprintf("Projected savings: -%.4f kg/day (-%.1f%%)", savings, pct)))
	} else {
		pdf.SetTextColor(200, 30, 30)
		pdf.Cell(0, 6, pdfText(fmt.Sprintf("Projected regression: +%.4f kg/day (+%.1f%%)", -savings, -pct)))
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(6)

	// Back to portrait for subsequent sections.
	pdf.AddPage()
}

// ── Section: ops series chart ─────────────────────────────────────────────────

func pdfOpsChart(pdf *gofpdf.Fpdf, m model) {
	pdfSectionHeading(pdf, "Operational Emissions (24h)")
	if len(m.opsSeries) == 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(120, 120, 120)
		pdf.Cell(0, 5, "(no time-series data available)")
		pdf.SetTextColor(0, 0, 0)
		pdf.Ln(5)
		return
	}

	x, y := pdf.GetXY()
	chartW := 186.0
	chartH := 60.0
	drawOpsChart(pdf, m.opsSeries, m.simOpsSeries, x, y, chartW, chartH)
	pdf.SetXY(x, y+chartH+3)

	// Legend.
	pdf.SetFont("Helvetica", "", 8)
	pdf.SetTextColor(60, 60, 60)
	pdf.SetDrawColor(40, 80, 180)
	pdf.Line(x, pdf.GetY()+2, x+8, pdf.GetY()+2)
	pdf.SetXY(x+10, pdf.GetY())
	pdf.Cell(40, 4, "baseline")
	if len(m.simOpsSeries) > 0 {
		legX := x + 60
		pdf.SetDrawColor(34, 139, 34)
		pdf.Line(legX, pdf.GetY()+2, legX+8, pdf.GetY()+2)
		pdf.SetXY(legX+10, pdf.GetY())
		pdf.Cell(40, 4, "simulated")
	}
	pdf.SetTextColor(0, 0, 0)
	pdf.Ln(7)
}

func drawOpsChart(pdf *gofpdf.Fpdf, baseline, sim []OpsEmissionPoint, x, y, w, h float64) {
	// Bounding box.
	pdf.SetDrawColor(180, 180, 180)
	pdf.Rect(x, y, w, h, "D")

	// Compute min/max over both series.
	minV, maxV := chartRange(baseline, sim)
	if maxV-minV < 1e-9 {
		maxV = minV + 1
	}
	tMin, tMax := chartTimeRange(baseline, sim)
	if !tMax.After(tMin) {
		tMax = tMin.Add(24 * time.Hour)
	}

	// Y-axis labels (4 ticks).
	pdf.SetFont("Helvetica", "", 7)
	pdf.SetTextColor(110, 110, 110)
	for i := 0; i <= 4; i++ {
		v := minV + (maxV-minV)*float64(i)/4
		yy := y + h - h*float64(i)/4
		label := fmt.Sprintf("%.3f", v)
		pdf.Text(x-10, yy+1.2, label)
		pdf.SetDrawColor(230, 230, 230)
		pdf.Line(x, yy, x+w, yy)
	}

	// X-axis (start / end timestamps).
	pdf.Text(x, y+h+4, tMin.Local().Format("15:04"))
	endLabel := tMax.Local().Format("15:04")
	pdf.Text(x+w-12, y+h+4, endLabel)
	pdf.SetTextColor(0, 0, 0)

	plot := func(series []OpsEmissionPoint, r, g, b int) {
		if len(series) < 2 {
			return
		}
		pdf.SetDrawColor(r, g, b)
		pdf.SetLineWidth(0.4)
		spanT := tMax.Sub(tMin).Seconds()
		if spanT <= 0 {
			return
		}
		spanV := maxV - minV
		var prevX, prevY float64
		first := true
		for _, p := range series {
			fx := x + (p.Timestamp.Sub(tMin).Seconds()/spanT)*w
			fy := y + h - ((p.Emissions-minV)/spanV)*h
			if !first {
				pdf.Line(prevX, prevY, fx, fy)
			}
			prevX, prevY = fx, fy
			first = false
		}
	}
	plot(baseline, 40, 80, 180)
	plot(sim, 34, 139, 34)
	pdf.SetLineWidth(0.2)
}

func chartRange(a, b []OpsEmissionPoint) (float64, float64) {
	first := true
	var lo, hi float64
	for _, s := range [][]OpsEmissionPoint{a, b} {
		for _, p := range s {
			if first {
				lo, hi = p.Emissions, p.Emissions
				first = false
				continue
			}
			if p.Emissions < lo {
				lo = p.Emissions
			}
			if p.Emissions > hi {
				hi = p.Emissions
			}
		}
	}
	if first {
		return 0, 1
	}
	return lo, hi
}

func chartTimeRange(a, b []OpsEmissionPoint) (time.Time, time.Time) {
	var lo, hi time.Time
	first := true
	for _, s := range [][]OpsEmissionPoint{a, b} {
		for _, p := range s {
			if first {
				lo, hi = p.Timestamp, p.Timestamp
				first = false
				continue
			}
			if p.Timestamp.Before(lo) {
				lo = p.Timestamp
			}
			if p.Timestamp.After(hi) {
				hi = p.Timestamp
			}
		}
	}
	return lo, hi
}

// ── Section: regional matrix ──────────────────────────────────────────────────

func pdfMatrixTable(pdf *gofpdf.Fpdf, m model) {
	pdfSectionHeading(pdf, "Regional Trade-off Matrix")
	if len(m.matrixData) == 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(120, 120, 120)
		pdf.Cell(0, 5, "(matrix unavailable — check your ElectricityMaps token)")
		pdf.SetTextColor(0, 0, 0)
		pdf.Ln(5)
		return
	}

	headers := []string{"Region", "gCO2/kWh", "CO2 kg/day", "Ops Δ", "$/hr", "$/mo"}
	widths := []float64{58, 22, 24, 18, 22, 22}
	aligns := []string{"L", "R", "R", "R", "R", "R"}

	rows := make([][]string, 0, len(m.matrixData))
	for _, d := range m.matrixData {
		hr, mo := "—", "—"
		if d.CostKnown {
			hr = fmt.Sprintf("%.4f", d.HourlyCost)
			mo = fmt.Sprintf("%.2f", d.MonthlyCost)
		}
		rows = append(rows, []string{
			d.RegionName,
			fmt.Sprintf("%.2f", d.Intensity),
			fmt.Sprintf("%.4f", d.Total),
			d.DeltaStr, hr, mo,
		})
	}
	pdfTable(pdf, headers, widths, aligns, rows, -1)
	pdf.Ln(4)
}

// ── Section: cost comparison ──────────────────────────────────────────────────

func pdfCostTables(pdf *gofpdf.Fpdf, m model) {
	pdfSectionHeading(pdf, "Regional Cost Comparison")
	if len(m.baselineCost.Resources) == 0 {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(120, 120, 120)
		msg := "pricing comparison unavailable"
		if m.pricingWarn != "" {
			msg = m.pricingWarn
		}
		pdf.MultiCell(0, 5, pdfText(msg), "", "", false)
		pdf.SetTextColor(0, 0, 0)
		pdf.Ln(2)
		return
	}

	headers := []string{"Resource", "Base $/hr", "Sim $/hr", "Δ $/hr", "Status"}
	widths := []float64{70, 24, 24, 22, 32}
	aligns := []string{"L", "R", "R", "R", "L"}

	targetByAddress := make(map[string]CostResource)
	for _, r := range m.targetCost.Resources {
		targetByAddress[r.Address] = r
	}

	rows := make([][]string, 0, len(m.baselineCost.Resources)+1)
	for _, b := range m.baselineCost.Resources {
		label := fmt.Sprintf("%s.%s", b.Type, b.Name)
		target, hasTarget := targetByAddress[b.Address]
		currStr, targetStr, deltaStr, status := "—", "—", "—", "price unavailable"
		if b.Available {
			currStr = fmt.Sprintf("%.5f", b.Hourly)
		}
		if hasTarget && target.Available {
			targetStr = fmt.Sprintf("%.5f", target.Hourly)
		}
		if b.Available && hasTarget && target.Available {
			d := target.Hourly - b.Hourly
			if d >= 0 {
				deltaStr = fmt.Sprintf("+%.5f", d)
			} else {
				deltaStr = fmt.Sprintf("%.5f", d)
			}
			status = "estimated"
		}
		if !hasTarget {
			if m.simDone {
				status = "missing target row"
			} else {
				status = "baseline only"
			}
		}
		rows = append(rows, []string{label, currStr, targetStr, deltaStr, status})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })
	rows = append(rows, []string{
		"-- TOTALS --",
		fmt.Sprintf("%.5f", m.baselineCost.TotalHourly),
		func() string {
			if m.targetCost.Region == "" {
				return "—"
			}
			return fmt.Sprintf("%.5f", m.targetCost.TotalHourly)
		}(),
		func() string {
			if m.targetCost.Region == "" {
				return "—"
			}
			d := m.targetCost.TotalHourly - m.baselineCost.TotalHourly
			if d >= 0 {
				return fmt.Sprintf("+%.5f", d)
			}
			return fmt.Sprintf("%.5f", d)
		}(),
		"hourly",
	})

	pdfTable(pdf, headers, widths, aligns, rows, len(rows)-1)

	pdf.Ln(2)
	pdf.SetFont("Helvetica", "", 9)
	pdf.MultiCell(0, 5, pdfText(fmt.Sprintf("Current (%s): $%.4f/hr  |  $%.2f/mo",
		m.baselineCost.Region, m.baselineCost.TotalHourly, m.baselineCost.TotalMonthly)), "", "", false)
	if m.targetCost.Region != "" {
		dm := m.targetCost.TotalMonthly - m.baselineCost.TotalMonthly
		sign := "+"
		if dm < 0 {
			sign = ""
		}
		pdf.MultiCell(0, 5, pdfText(fmt.Sprintf("Target  (%s): $%.4f/hr  |  $%.2f/mo  (%s%.2f/mo)",
			m.targetCost.Region, m.targetCost.TotalHourly, m.targetCost.TotalMonthly, sign, dm)), "", "", false)
	}
	if m.baselineCost.UnavailableCount > 0 || m.targetCost.UnavailableCount > 0 {
		pdf.MultiCell(0, 5, pdfText(fmt.Sprintf("Unavailable prices: current %d, target %d",
			m.baselineCost.UnavailableCount, m.targetCost.UnavailableCount)), "", "", false)
	}
	if m.pricingWarn != "" {
		pdf.SetTextColor(180, 130, 0)
		pdf.MultiCell(0, 5, pdfText(m.pricingWarn), "", "", false)
		pdf.SetTextColor(0, 0, 0)
	}
	pdf.Ln(3)
}

// ── Section: AI recommendations ───────────────────────────────────────────────

func pdfAISection(pdf *gofpdf.Fpdf, m model) {
	pdfSectionHeading(pdf, "AI GreenOps Recommendations")
	body := m.aiContentRaw
	if body == "" {
		body = stripANSI(m.aiContent)
	}
	if strings.TrimSpace(body) == "" {
		pdf.SetFont("Helvetica", "I", 9)
		pdf.SetTextColor(120, 120, 120)
		pdf.Cell(0, 5, "(no AI recommendations available)")
		pdf.SetTextColor(0, 0, 0)
		pdf.Ln(5)
		return
	}
	pdf.SetFont("Helvetica", "", 10)
	// Markdown is rendered as plain text; we leave structure to the reader.
	pdf.MultiCell(0, 5, pdfText(body), "", "", false)
	pdf.Ln(2)
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(s string) string { return ansiRe.ReplaceAllString(s, "") }

// pdfText converts an arbitrary UTF-8 string into something the built-in
// Helvetica (WinAnsi) font can render. Characters outside WinAnsi are
// transliterated where we know a sensible ASCII equivalent and dropped
// otherwise — gofpdf would otherwise render them as "?".
var pdfTranslit = strings.NewReplacer(
	"★", "*",
	"→", "->",
	"←", "<-",
	"↔", "<->",
	"≤", "<=",
	"≥", ">=",
	"Δ", "D",
	"²", "2", "³", "3",
	"₀", "0", "₁", "1", "₂", "2", "₃", "3", "₄", "4",
	"₅", "5", "₆", "6", "₇", "7", "₈", "8", "₉", "9",
	"·", "-", "•", "-",
	"…", "...",
)

func pdfText(s string) string {
	s = pdfTranslit.Replace(s)
	// Drop any remaining non-WinAnsi runes (covers emoji etc.).
	b := make([]rune, 0, len(s))
	for _, r := range s {
		if r > 0xFF && r != '\n' && r != '\t' {
			continue
		}
		b = append(b, r)
	}
	return string(b)
}

// ── Shared layout primitives ──────────────────────────────────────────────────

func pdfSectionHeading(pdf *gofpdf.Fpdf, title string) {
	pdf.SetFont("Helvetica", "B", 13)
	pdf.SetTextColor(34, 139, 34)
	pdf.Cell(0, 7, pdfText(title))
	pdf.Ln(7)
	pdf.SetTextColor(0, 0, 0)
}

// pdfTable renders a table with a header row, a body, and an optional bold
// totals row (totalsRowIdx; pass -1 to skip). `aligns` must be the same
// length as `headers` ("L" / "C" / "R") and is applied to both the header
// and body cells of each column so the two stay aligned.
func pdfTable(pdf *gofpdf.Fpdf, headers []string, widths []float64, aligns []string, rows [][]string, totalsRowIdx int) {
	// Header.
	pdf.SetFont("Helvetica", "B", 9)
	pdf.SetFillColor(240, 248, 240)
	pdf.SetTextColor(0, 80, 0)
	for i, h := range headers {
		pdf.CellFormat(widths[i], 6, pdfText(h), "B", 0, aligns[i], true, 0, "")
	}
	pdf.Ln(-1)
	pdf.SetTextColor(0, 0, 0)

	// Body.
	pdf.SetFont("Helvetica", "", 9)
	for ri, row := range rows {
		bold := ri == totalsRowIdx
		if bold {
			pdf.SetFont("Helvetica", "B", 9)
		}
		for i, v := range row {
			pdf.CellFormat(widths[i], 5, truncatePDF(v, widths[i]), "", 0, aligns[i], false, 0, "")
		}
		pdf.Ln(-1)
		if bold {
			pdf.SetFont("Helvetica", "", 9)
		}
	}
}

// truncatePDF caps a cell value to ~widthMm so it doesn't bleed past
// neighbouring columns and sanitises any non-WinAnsi characters.
func truncatePDF(s string, widthMm float64) string {
	s = pdfText(s)
	maxChars := int(widthMm / 1.8) // ~1.8mm per char at Helvetica 9pt
	if maxChars < 4 {
		maxChars = 4
	}
	if len(s) <= maxChars {
		return s
	}
	if maxChars <= 1 {
		return s[:maxChars]
	}
	return s[:maxChars-1] + "..."
}
