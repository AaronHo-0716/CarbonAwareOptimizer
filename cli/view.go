package main

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/v2/linechart"
	"github.com/NimbleMarkets/ntcharts/v2/linechart/timeserieslinechart"
	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"

	lipglossv2 "charm.land/lipgloss/v2"
)

// ── Colour palette ────────────────────────────────────────────────────────────

var (
	clrGreen  = lipgloss.Color("#00BF72")
	clrBlue   = lipgloss.Color("#6272A4")
	clrDim    = lipgloss.Color("#555555")
	clrMuted  = lipgloss.Color("#888888")
	clrRed    = lipgloss.Color("#FF5555")
	clrWhite  = lipgloss.Color("#F8F8F2")
	clrYellow = lipgloss.Color("#F1FA8C")
)

// ── Reusable styles ───────────────────────────────────────────────────────────

var (
	titleBarStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(clrWhite).
			Background(clrGreen).
			PaddingLeft(2).PaddingRight(2)

	sectionHeadStyle = lipgloss.NewStyle().Bold(true).Foreground(clrGreen)
	mutedStyle       = lipgloss.NewStyle().Foreground(clrMuted)
	dimStyle         = lipgloss.NewStyle().Foreground(clrDim)
	redStyle         = lipgloss.NewStyle().Foreground(clrRed)
	yellowStyle      = lipgloss.NewStyle().Foreground(clrYellow)
	greenBoldStyle   = lipgloss.NewStyle().Bold(true).Foreground(clrGreen)

	focusedBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(clrGreen)

	unfocusedBorderStyle = lipgloss.NewStyle().
				BorderStyle(lipgloss.RoundedBorder()).
				BorderForeground(clrDim)

	popupStyle = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(clrGreen).
			Padding(1, 3)

	btnNormalStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Background(clrDim).
			Foreground(clrWhite)

	btnFocusedStyle = lipgloss.NewStyle().
			Padding(0, 2).
			Background(clrGreen).
			Foreground(clrWhite).
			Bold(true)
)

// ── View dispatcher ───────────────────────────────────────────────────────────

func (m model) View() string {
	if m.width == 0 {
		return "Initialising…"
	}
	switch m.state {
	case stateAPIKeys:
		return m.viewAPIKeys()
	case stateS3Probing:
		return m.viewS3Probing()
	case stateS3Source:
		return m.viewS3Source()
	case stateS3CreateBucket:
		return m.viewS3CreateBucket()
	case stateS3SlugInput:
		return m.viewS3Slug()
	case stateS3DeleteConfirm:
		return m.viewS3DeleteConfirm()
	case stateFilePicker:
		return m.viewFilePicker()
	case stateLoading:
		return m.viewLoading()
	case stateResults:
		return m.viewResults()
	}
	return ""
}

// viewS3Probing is shown for the brief moment the AWS tag lookup is in flight.
func (m model) viewS3Probing() string {
	body := strings.Join([]string{
		greenBoldStyle.Render("🌍  Carbon Optimizer"),
		"",
		m.spinner.View() + " Looking for a Carbon-Optimizer S3 bucket…",
		"",
		dimStyle.Render("(uses your default AWS credentials)"),
	}, "\n")
	popup := popupStyle.Width(60).Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, popup)
}

// ── API-keys popup (centred overlay) ─────────────────────────────────────────

func (m model) viewAPIKeys() string {
	cursor := func(focused bool) string {
		if focused {
			return greenBoldStyle.Render("▶ ")
		}
		return "  "
	}

	emLabel := mutedStyle.Render("🌿  ElectricityMaps Token")
	orLabel := mutedStyle.Render("🤖  OpenRouter API Key") +
		"  " + dimStyle.Render("(optional — leave blank to skip)")

	errLine := ""
	if m.keyError != "" {
		errLine = "\n" + redStyle.Render("   ⚠  "+m.keyError)
	}

	body := strings.Join([]string{
		greenBoldStyle.Render("🌍  Carbon Optimizer — API Configuration"),
		"",
		emLabel,
		cursor(m.apiKeyFocus == 0) + m.emInput.View(),
		"",
		orLabel,
		cursor(m.apiKeyFocus == 1) + m.orInput.View(),
		errLine,
		"",
		dimStyle.Render("Tab · Switch field    Enter · Confirm    Esc · Quit"),
	}, "\n")

	popup := popupStyle.Width(60).Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, popup)
}

// ── File-picker view ──────────────────────────────────────────────────────────

func (m model) viewFilePicker() string {
	header := titleBarStyle.Width(m.width).Render(
		"🌍  Carbon Optimizer  ·  Select your Terraform Plan",
	)

	subHeader := mutedStyle.Render(
		"  Select a plan.json (pre-computed) or a project directory to run Terraform automatically",
	)

	// Current directory breadcrumb
	cwdLine := dimStyle.Render("  📁 " + m.fp.currentDir)

	errLine := ""
	if m.errMsg != "" {
		errLine = "  " + redStyle.Render("⚠  "+m.errMsg)
	}

	// Reserve 2 extra rows for the breadcrumb + error lines
	fpBox := unfocusedBorderStyle.
		Width(m.width - 2).
		Height(m.height - 8).
		Render(m.fp.View())

	help := mutedStyle.Render(
		"  ↑ ↓ / j k · Navigate    Enter / → · Open    ← h Backspace · Go up    ~ · Home    q · Quit",
	)

	lines := []string{header, subHeader, cwdLine}
	if errLine != "" {
		lines = append(lines, errLine)
	}
	lines = append(lines, fpBox, help)

	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// ── Loading view ──────────────────────────────────────────────────────────────

func (m model) viewLoading() string {
	inner := m.spinner.View() + "  " +
		lipgloss.NewStyle().Foreground(clrGreen).Render(m.loadMsg)

	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, inner)
}

// ── Results view ──────────────────────────────────────────────────────────────

func (m model) viewResults() string {
	if m.errMsg != "" {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center,
			redStyle.Render("Error: "+m.errMsg))
	}

	totalDaily := m.totalOps + m.totalEmb

	// ── Title bar ──
	titleBar := titleBarStyle.Width(m.width).Render(fmt.Sprintf(
		"🌍  Carbon Optimizer   ·   %s   ·   %.1f gCO₂e/kWh   ·   Total: %.4f kg CO₂/day",
		m.region, m.gridIntensity, totalDaily,
	))

	// ── Main viewport panel ──
	mainBorder := unfocusedBorderStyle
	if m.focus == focusMain {
		mainBorder = focusedBorderStyle
	}
	var mainPanel string
	if m.mainVPReady {
		mainPanel = mainBorder.
			Width(m.vpWidth()).
			Height(m.vpHeight()).
			Render(m.mainVP.View())
	} else {
		mainPanel = mainBorder.
			Width(m.vpWidth()).
			Height(m.vpHeight()).
			Render(mutedStyle.Render("Loading…"))
	}

	// ── Side panel ──
	sidePanel := m.renderSidePanel()

	// ── Combine horizontally ──
	body := lipgloss.JoinHorizontal(lipgloss.Top, mainPanel, sidePanel)

	// ── Help bar ──
	scrollPct := 0
	if m.mainVPReady {
		scrollPct = int(m.mainVP.ScrollPercent() * 100)
	}
	focusHint := "↑ ↓  Scroll"
	if m.focus == focusSide {
		focusHint = "↑ ↓  Navigate panel    ← →  Cycle option    Enter · Select"
	}
	help := mutedStyle.Width(m.width).Render(fmt.Sprintf(
		"Tab · Switch panel    %s    f · New file    q · Quit    %d%%",
		focusHint, scrollPct,
	))

	return lipgloss.JoinVertical(lipgloss.Left, titleBar, body, help)
}

// ── Side panel ────────────────────────────────────────────────────────────────

func (m model) renderSidePanel() string {
	body := m.renderSidePanelBody()
	isFocused := m.focus == focusSide

	borderStyle := unfocusedBorderStyle
	if isFocused {
		borderStyle = focusedBorderStyle
	}

	vp := m.sideVP
	vp.Width = sideW - 2
	vp.Height = m.vpHeight()
	vp.SetContent(body)
	vp.SetYOffset(sideVPOffsetForFocus(body, m.sideVP.YOffset, vp.Height))

	return borderStyle.
		Width(sideW - 2).
		Height(m.vpHeight()).
		Render(vp.View())
}

// renderSidePanelBody is the pure string-builder for the side panel content,
// excluding the surrounding border + viewport. Kept separate so Update can
// compute scroll offsets without round-tripping through render.
func (m model) renderSidePanelBody() string {
	active := m.activeSideItems()
	isFocused := m.focus == focusSide

	isActive := func(item sideItem) bool {
		for _, it := range active {
			if it == item {
				return true
			}
		}
		return false
	}

	cursor := func(item sideItem) string {
		if isFocused && m.sideFocused == item {
			return greenBoldStyle.Render("▶ ")
		}
		return "  "
	}

	btn := func(label string, item sideItem) string {
		if isFocused && m.sideFocused == item {
			return btnFocusedStyle.Render(label)
		}
		return btnNormalStyle.Render(label)
	}

	var b strings.Builder

	// ── Settings section ───────────────────────────────────────────────────────
	b.WriteString(sectionHeadStyle.Render("⚙  Settings") + "\n\n")

	if isActive(sideNetworkProfile) {
		profileLabel := map[string]string{
			"low":    "Low   ( 10 GB/mo)",
			"medium": "Med  (100 GB/mo)",
			"high":   "High  ( 1 TB/mo)",
		}[m.networkProfile]

		focused := isFocused && m.sideFocused == sideNetworkProfile
		b.WriteString(cursor(sideNetworkProfile) + mutedStyle.Render("Network Traffic") + "\n")
		if focused {
			b.WriteString("  " + greenBoldStyle.Render("◀ "+profileLabel+" ▶") + "\n\n")
		} else {
			b.WriteString("  " + dimStyle.Render("◀") + " " + profileLabel + " " + dimStyle.Render("▶") + "\n\n")
		}
	}

	if isActive(sideLambdaInput) {
		b.WriteString(cursor(sideLambdaInput) + mutedStyle.Render("Lambda Invoc./day") + "\n")
		b.WriteString("  " + m.lambdaInput.View() + "\n\n")
	}

	if isActive(sideWorkStartHour) {
		b.WriteString(cursor(sideWorkStartHour) + mutedStyle.Render("Work start (0-23)") + "\n")
		b.WriteString("  " + m.workStartInput.View() + "\n\n")
	}
	if isActive(sideWorkEndHour) {
		b.WriteString(cursor(sideWorkEndHour) + mutedStyle.Render("Work end (1-24)") + "\n")
		b.WriteString("  " + m.workEndInput.View() + "\n\n")
	}
	if isActive(sideWorkUtilPct) {
		b.WriteString(cursor(sideWorkUtilPct) + mutedStyle.Render("Work util (%)") + "\n")
		b.WriteString("  " + m.workUtilInput.View() + "\n\n")
	}
	if isActive(sideIdleUtilPct) {
		b.WriteString(cursor(sideIdleUtilPct) + mutedStyle.Render("Idle util (%)") + "\n")
		b.WriteString("  " + m.idleUtilInput.View() + "\n\n")
	}

	if isActive(sideReanalyze) {
		b.WriteString(cursor(sideReanalyze) + btn("  Re-analyse  ", sideReanalyze) + "\n\n")
	}
	b.WriteString(dimStyle.Render(strings.Repeat("─", sideW-4)) + "\n\n")

	// ── Optimisation section ──────────────────────────────────────────────────
	b.WriteString(sectionHeadStyle.Render("🔧  Optimise") + "\n\n")

	// Graviton radio
	gDot := "○"
	if m.optType == "graviton" {
		gDot = greenBoldStyle.Render("●")
	}
	b.WriteString(cursor(sideOptGraviton) + gDot + " Migrate → Graviton\n")

	// Region radio
	rDot := "○"
	if m.optType == "region" {
		rDot = greenBoldStyle.Render("●")
	}
	b.WriteString(cursor(sideOptRegion) + rDot + " Move to Greener Region\n")

	if isActive(sideRegionInput) {
		b.WriteString("\n")
		b.WriteString(cursor(sideRegionInput) + mutedStyle.Render("Target region") + "\n")
		b.WriteString("  " + m.regionInput.View() + "\n")
	}

	b.WriteString("\n")
	b.WriteString(cursor(sideApply) + btn("     Apply      ", sideApply) + "\n")
	b.WriteString("\n")
	b.WriteString(cursor(sideExportPDF) + btn(" Export PDF (^P) ", sideExportPDF) + "\n")
	if m.lastExportMsg != "" {
		b.WriteString(mutedStyle.Render(m.lastExportMsg) + "\n")
	}
	b.WriteString("\n")
	b.WriteString("  " + mutedStyle.Render("Ctrl+S · Save to S3") + "\n")
	if m.lastUploadMsg != "" {
		b.WriteString(mutedStyle.Render(m.lastUploadMsg) + "\n")
	}

	// ── Simulation result ─────────────────────────────────────────────────────
	if m.simDone {
		b.WriteString("\n" + dimStyle.Render(strings.Repeat("─", sideW-4)) + "\n\n")
		b.WriteString(sectionHeadStyle.Render("📊  Projected Savings") + "\n\n")

		origTotal := m.totalOps + m.totalEmb
		savings := origTotal - m.simTotal
		pct := 0.0
		if origTotal > 0 {
			pct = (savings / origTotal) * 100
		}
		b.WriteString(fmt.Sprintf("  Before  %.4f kg/day\n", origTotal))
		b.WriteString(fmt.Sprintf("  After   %.4f kg/day\n\n", m.simTotal))

		if savings > 0 {
			b.WriteString(greenBoldStyle.Render(
				fmt.Sprintf("  ✨ −%.4f kg  (%.1f%%)", savings, pct)) + "\n")
		} else {
			b.WriteString(redStyle.Render(
				fmt.Sprintf("  ⚠  +%.4f kg  (+%.1f%%)", -savings, -pct)) + "\n")
		}

		if m.baselineCost.Region != "" && m.targetCost.Region != "" {
			baseMonthly := m.baselineCost.TotalMonthly
			targetMonthly := m.targetCost.TotalMonthly
			costDelta := targetMonthly - baseMonthly
			prefix := "+"
			if costDelta < 0 {
				prefix = ""
			}
			b.WriteString("\n")
			b.WriteString(fmt.Sprintf("  Cost before  $%.2f/mo\n", baseMonthly))
			b.WriteString(fmt.Sprintf("  Cost after   $%.2f/mo\n", targetMonthly))
			if costDelta <= 0 {
				b.WriteString(greenBoldStyle.Render(fmt.Sprintf("  💰 %s%.2f/mo", prefix, costDelta)) + "\n")
			} else {
				b.WriteString(redStyle.Render(fmt.Sprintf("  💰 +%.2f/mo", costDelta)) + "\n")
			}
		}
	}

	return b.String()
}

// sideVPOffsetForFocus returns a YOffset that keeps the focus marker visible
// within a window of `h` lines. If the focus marker isn't present (i.e. the
// side panel isn't focused), it preserves the existing offset.
func sideVPOffsetForFocus(body string, currentOffset, h int) int {
	if h < 1 {
		return 0
	}
	marker := greenBoldStyle.Render("▶ ")
	idx := strings.Index(body, marker)
	if idx < 0 {
		// No focus; clamp existing offset to content range.
		lines := strings.Count(body, "\n") + 1
		if currentOffset+h > lines {
			currentOffset = lines - h
		}
		if currentOffset < 0 {
			currentOffset = 0
		}
		return currentOffset
	}
	focusLine := strings.Count(body[:idx], "\n")
	if focusLine < currentOffset {
		return focusLine
	}
	if focusLine >= currentOffset+h {
		return focusLine - h + 1
	}
	return currentOffset
}


// ── Main viewport content ─────────────────────────────────────────────────────

func (m model) buildMainContent() string {
	schedule := normalizeSchedule(m.utilization)
	workHours := scheduleWorkHours(schedule.WorkStartHour, schedule.WorkEndHour)
	idleHours := 24 - workHours
	parts := []string{
		sectionHeadStyle.Render("⏱  Utilization Schedule") + "\n" +
			fmt.Sprintf("Every day: %02d:00-%02d:00 work (%d h @ %.0f%%), idle (%d h @ %.0f%%)",
				schedule.WorkStartHour, schedule.WorkEndHour, workHours, schedule.WorkPct, idleHours, schedule.IdlePct),
		"",
		m.buildImpactTable(),
		"",
		m.buildOpsSeriesChart(),
		"",
		m.buildMatrixTable(),
		"",
		m.buildCostTable(),
		"",
		m.buildAISection(),
	}
	return strings.Join(parts, "\n")
}

// ── Impact table ──────────────────────────────────────────────────────────────

func (m model) buildImpactTable() string {
	// table.DefaultStyles() gives every cell Padding(0,1). In lipgloss, Width()
	// sets the *content* area (excl. padding), so each column renders col_width+2
	// chars at runtime. We must subtract that from the available space or the
	// table overflows the viewport.

	// Side-by-side comparison view: only render when sim is done, sim data
	// is present, and the viewport is wide enough to fit all 12 columns.
	wide := m.simDone && len(m.simImpacts) > 0
	if wide {
		// Overhead: border(2) + padding(2) + 12 cols × cell padding(2) = 28
		const wideOverhead = 2 + 2 + 12*2
		const wideFixed = 9 + 9 + 3 + 7 + 7 + 7 + 7 + 8 + 8 + 7 // 72
		if m.vpWidth()-wideOverhead-wideFixed < 12 {
			wide = false // not enough room for Type+Name; fall back
		}
	}

	if wide {
		return m.buildImpactTableWide()
	}

	const overhead = 2 + 2 + 7*2 // = 18
	avail := m.vpWidth() - overhead
	if avail < 50 {
		avail = 50
	}

	instW, qtyW, opsW, embW, totW := 12, 5, 9, 9, 10
	fixed := instW + qtyW + opsW + embW + totW // 45
	dyn := avail - fixed
	if dyn < 16 {
		dyn = 16
	}
	typeW := dyn / 2
	nameW := dyn - typeW

	cols := []table.Column{
		{Title: "Resource Type", Width: typeW},
		{Title: "Name", Width: nameW},
		{Title: "Instance", Width: instW},
		{Title: "Qty", Width: qtyW},
		{Title: "Ops kg", Width: opsW},
		{Title: "Emb kg", Width: embW},
		{Title: "Total kg", Width: totW},
	}

	var rows []table.Row
	for _, imp := range m.impacts {
		rows = append(rows, table.Row{
			truncate(imp.Type, typeW-1),
			truncate(imp.Name, nameW-1),
			imp.Instance,
			fmt.Sprintf("%d", imp.Count),
			fmt.Sprintf("%.4f", imp.DailyOps),
			fmt.Sprintf("%.4f", imp.DailyEmb),
			fmt.Sprintf("%.4f", imp.TotalDaily),
		})
	}
	// Totals footer row
	rows = append(rows, table.Row{
		"── TOTALS ──", "", "", "",
		fmt.Sprintf("%.4f", m.totalOps),
		fmt.Sprintf("%.4f", m.totalEmb),
		fmt.Sprintf("%.4f", m.totalOps+m.totalEmb),
	})

	ts := table.DefaultStyles()
	ts.Header = ts.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(clrDim).
		BorderBottom(true).Bold(true).
		Foreground(clrGreen)
	ts.Selected = lipgloss.NewStyle() // disable row highlight (read-only table)

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithHeight(len(rows)+2),
	)
	t.SetStyles(ts)

	return focusedBorderStyle.
		BorderForeground(clrGreen).
		Padding(0, 1).
		Render(sectionHeadStyle.Render("📊  Resource Carbon Impact") + "\n\n" + t.View())
}

// buildImpactTableWide renders the 12-column baseline/sim comparison.
// Columns are interleaved so each baseline metric sits next to its sim counterpart.
func (m model) buildImpactTableWide() string {
	const wideOverhead = 2 + 2 + 12*2
	instW, simInstW, qtyW := 9, 9, 3
	opsW, simOpsW := 7, 7
	embW, simEmbW := 7, 7
	totW, simTotW := 8, 8
	pctW := 7
	fixed := instW + simInstW + qtyW + opsW + simOpsW + embW + simEmbW + totW + simTotW + pctW
	avail := m.vpWidth() - wideOverhead
	dyn := avail - fixed
	if dyn < 12 {
		dyn = 12
	}

	// Cap left-side Type+Name and redistribute the surplus across the right-side
	// numeric columns so they don't feel cramped on wide terminals.
	const maxLeft = 28
	if dyn > maxLeft {
		extra := dyn - maxLeft
		dyn = maxLeft
		each := extra / 9 // 9 right-side cols share the extra space
		rem := extra - each*9
		instW += each
		simInstW += each
		opsW += each
		simOpsW += each
		embW += each
		simEmbW += each
		totW += each
		simTotW += each
		pctW += each + rem
	}

	typeW := dyn / 2
	nameW := dyn - typeW

	widths := []int{typeW, nameW, instW, simInstW, qtyW, opsW, simOpsW, embW, simEmbW, totW, simTotW, pctW}
	headers := []string{"Resource Type", "Name", "Instance", "Sim Inst", "Qty", "Ops kg", "Sim Ops", "Emb kg", "Sim Emb", "Total kg", "Sim Tot", "Δ %"}
	// Bold the sim and delta columns (indexes match `headers`).
	boldCol := map[int]bool{3: true, 6: true, 8: true, 10: true, 11: true}

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

	var rows [][]string
	for _, imp := range m.impacts {
		sim, ok := simByKey[imp.Type+"\x00"+imp.Name]
		simInst, simOps, simEmb, simTot, pct := "—", "—", "—", "—", "—"
		if ok {
			simInst = sim.Instance
			simOps = fmt.Sprintf("%.4f", sim.DailyOps)
			simEmb = fmt.Sprintf("%.4f", sim.DailyEmb)
			simTot = fmt.Sprintf("%.4f", sim.TotalDaily)
			pct = fmtPct(imp.TotalDaily, sim.TotalDaily)
		}
		rows = append(rows, []string{
			imp.Type, imp.Name, imp.Instance, simInst,
			fmt.Sprintf("%d", imp.Count),
			fmt.Sprintf("%.4f", imp.DailyOps), simOps,
			fmt.Sprintf("%.4f", imp.DailyEmb), simEmb,
			fmt.Sprintf("%.4f", imp.TotalDaily), simTot,
			pct,
		})
	}
	baseTotal := m.totalOps + m.totalEmb
	rows = append(rows, []string{
		"── TOTALS ──", "", "", "", "",
		fmt.Sprintf("%.4f", m.totalOps),
		fmt.Sprintf("%.4f", m.simOps),
		fmt.Sprintf("%.4f", m.totalEmb),
		fmt.Sprintf("%.4f", m.simEmb),
		fmt.Sprintf("%.4f", baseTotal),
		fmt.Sprintf("%.4f", m.simTotal),
		fmtPct(baseTotal, m.simTotal),
	})

	// Manual row rendering. We can't go through bubbles/table here because its
	// renderRow runs runewidth.Truncate on cell values; that helper is ANSI-
	// unaware and would chop the bold escape codes mid-sequence, breaking the
	// table's visible width and pushing the side panel off-screen.
	renderCell := func(value string, width int, bold, header bool) string {
		style := lipgloss.NewStyle().Width(width).Inline(true)
		if bold || header {
			style = style.Bold(true)
		}
		if header {
			style = style.Foreground(clrGreen)
		}
		return " " + style.Render(truncate(value, width)) + " "
	}
	renderRow := func(values []string, header bool) string {
		cells := make([]string, len(values))
		for i, v := range values {
			cells[i] = renderCell(v, widths[i], boldCol[i], header)
		}
		return lipgloss.JoinHorizontal(lipgloss.Top, cells...)
	}

	totalW := 0
	for _, w := range widths {
		totalW += w + 2
	}
	sep := lipgloss.NewStyle().Foreground(clrDim).Render(strings.Repeat("─", totalW))

	bodyLines := []string{renderRow(headers, true), sep}
	for _, row := range rows {
		bodyLines = append(bodyLines, renderRow(row, false))
	}
	body := strings.Join(bodyLines, "\n")

	header := sectionHeadStyle.Render("📊  Resource Carbon Impact") +
		"  " + mutedStyle.Render(fmt.Sprintf("(baseline ↔ optimized · %s)", m.simRegion))
	return focusedBorderStyle.
		BorderForeground(clrGreen).
		Padding(0, 1).
		Render(header + "\n\n" + body)
}

// ── Regional matrix table ─────────────────────────────────────────────────────

func (m model) buildMatrixTable() string {
	if len(m.matrixData) == 0 {
		return mutedStyle.Render("  (regional matrix unavailable — check your ElectricityMaps token)")
	}

	// Overhead: box border(2) + box padding(2) + 6 cols × cell padding(2) = 16
	const overhead = 2 + 2 + 6*2 // = 16
	avail := m.vpWidth() - overhead
	if avail < 72 {
		avail = 72
	}

	intW, totW, deltaW, hrW, moW := 16, 11, 9, 10, 11
	fixed := intW + totW + deltaW + hrW + moW
	regionW := avail - fixed
	if regionW < 18 {
		regionW = 18
	}

	cols := []table.Column{
		{Title: "Region", Width: regionW},
		{Title: "Grid gCO₂/kWh", Width: intW},
		{Title: "CO₂ kg/day", Width: totW},
		{Title: "Ops Δ", Width: deltaW},
		{Title: "$/hr", Width: hrW},
		{Title: "$/mo", Width: moW},
	}

	var rows []table.Row
	for _, d := range m.matrixData {
		rows = append(rows, table.Row{
			truncate(d.RegionName, regionW-1),
			fmt.Sprintf("%.2f", d.Intensity),
			fmt.Sprintf("%.4f", d.Total),
			d.DeltaStr,
			func() string {
				if !d.CostKnown {
					return "—"
				}
				return fmt.Sprintf("%.4f", d.HourlyCost)
			}(),
			func() string {
				if !d.CostKnown {
					return "—"
				}
				return fmt.Sprintf("%.2f", d.MonthlyCost)
			}(),
		})
	}

	ts := table.DefaultStyles()
	ts.Header = ts.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(clrDim).
		BorderBottom(true).Bold(true).
		Foreground(clrGreen)
	ts.Selected = lipgloss.NewStyle()

	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithHeight(len(rows)+2),
	)
	t.SetStyles(ts)

	return focusedBorderStyle.
		BorderForeground(clrBlue).
		Padding(0, 1).
		Render(sectionHeadStyle.Render("🗺  Regional Trade-off Matrix (same continent)") + "\n\n" + t.View())
}

// ── AI section ────────────────────────────────────────────────────────────────

func (m model) buildAISection() string {
	if m.aiLoading {
		return "\n" + yellowStyle.Render("  🤖  Fetching AI GreenOps insights…  (this may take a moment)") + "\n"
	}
	if m.aiContent == "" {
		return ""
	}
	heading := sectionHeadStyle.Render("🌿  AI GreenOps Recommendations")
	return "\n" + heading + "\n" + m.aiContent
}

func (m model) buildCostTable() string {
	if len(m.baselineCost.Resources) == 0 {
		msg := "pricing comparison unavailable"
		if m.pricingWarn != "" {
			msg = m.pricingWarn
		}
		return focusedBorderStyle.
			BorderForeground(clrYellow).
			Padding(0, 1).
			Render(sectionHeadStyle.Render("💵  Regional Cost Comparison") + "\n\n" + yellowStyle.Render(msg))
	}

	overhead := 2 + 2 + 5*2
	avail := m.vpWidth() - overhead
	if avail < 62 {
		avail = 62
	}
	currW, targetW, deltaW, statusW := 12, 12, 10, 14
	nameW := avail - (currW + targetW + deltaW + statusW)
	if nameW < 22 {
		nameW = 22
	}

	cols := []table.Column{
		{Title: "Resource", Width: nameW},
		{Title: "Base $/hr", Width: currW},
		{Title: "Sim $/hr", Width: targetW},
		{Title: "Δ $/hr", Width: deltaW},
		{Title: "Status", Width: statusW},
	}

	targetByAddress := make(map[string]CostResource)
	for _, r := range m.targetCost.Resources {
		targetByAddress[r.Address] = r
	}

	var rows []table.Row
	for _, b := range m.baselineCost.Resources {
		label := truncate(fmt.Sprintf("%s.%s", b.Type, b.Name), nameW-1)
		target, hasTarget := targetByAddress[b.Address]
		currStr := "—"
		targetStr := "—"
		deltaStr := "—"
		status := "price unavailable"

		if b.Available {
			currStr = fmt.Sprintf("%.5f", b.Hourly)
		}
		if hasTarget && target.Available {
			targetStr = fmt.Sprintf("%.5f", target.Hourly)
		}
		if b.Available && hasTarget && target.Available {
			delta := target.Hourly - b.Hourly
			if delta >= 0 {
				deltaStr = fmt.Sprintf("+%.5f", delta)
			} else {
				deltaStr = fmt.Sprintf("%.5f", delta)
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
		rows = append(rows, table.Row{label, currStr, targetStr, deltaStr, truncate(status, statusW-1)})
	}

	if len(rows) == 0 {
		rows = append(rows, table.Row{"(no managed resources)", "—", "—", "—", "—"})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i][0] < rows[j][0] })

	rows = append(rows, table.Row{
		"── TOTALS ──",
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

	ts := table.DefaultStyles()
	ts.Header = ts.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(clrDim).
		BorderBottom(true).Bold(true).
		Foreground(clrGreen)
	ts.Selected = lipgloss.NewStyle()
	t := table.New(
		table.WithColumns(cols),
		table.WithRows(rows),
		table.WithHeight(len(rows)+2),
	)
	t.SetStyles(ts)

	var summary strings.Builder
	summary.WriteString(fmt.Sprintf("Current (%s): $%.4f/hr  |  $%.2f/mo", m.baselineCost.Region, m.baselineCost.TotalHourly, m.baselineCost.TotalMonthly))
	if m.targetCost.Region != "" {
		dm := m.targetCost.TotalMonthly - m.baselineCost.TotalMonthly
		sign := "+"
		if dm < 0 {
			sign = ""
		}
		summary.WriteString(fmt.Sprintf("\nTarget (%s):  $%.4f/hr  |  $%.2f/mo  (%s%.2f/mo)", m.targetCost.Region, m.targetCost.TotalHourly, m.targetCost.TotalMonthly, sign, dm))
	}
	if m.baselineCost.UnavailableCount > 0 || m.targetCost.UnavailableCount > 0 {
		summary.WriteString(fmt.Sprintf("\nUnavailable prices: current %d, target %d", m.baselineCost.UnavailableCount, m.targetCost.UnavailableCount))
	}
	if m.pricingWarn != "" {
		summary.WriteString("\n" + yellowStyle.Render(m.pricingWarn))
	}

	return focusedBorderStyle.
		BorderForeground(clrBlue).
		Padding(0, 1).
		Render(sectionHeadStyle.Render("💵  Regional Cost Comparison") + "\n\n" + t.View() + "\n\n" + summary.String())
}

func (m model) buildOpsSeriesChart() string {
	if len(m.opsSeries) == 0 {
		return focusedBorderStyle.
			BorderForeground(clrBlue).
			Padding(0, 1).
			Render(sectionHeadStyle.Render("📈  Operational Emissions Time Series") + "\n\n" +
				mutedStyle.Render("No time-series emissions data available."))
	}

	hasSim := m.simDone && len(m.simOpsSeries) > 0

	chartW := m.vpWidth() - 8
	if chartW < 48 {
		chartW = 48
	}
	chartH := 10

	minT := m.opsSeries[0].Timestamp
	maxT := m.opsSeries[len(m.opsSeries)-1].Timestamp
	if hasSim {
		if m.simOpsSeries[0].Timestamp.Before(minT) {
			minT = m.simOpsSeries[0].Timestamp
		}
		if last := m.simOpsSeries[len(m.simOpsSeries)-1].Timestamp; last.After(maxT) {
			maxT = last
		}
	}
	if !maxT.After(minT) {
		maxT = minT.Add(time.Hour)
	}

	minY := math.Inf(1)
	maxY := math.Inf(-1)
	collectRange := func(series []OpsEmissionPoint) {
		for _, p := range series {
			if p.Emissions < minY {
				minY = p.Emissions
			}
			if p.Emissions > maxY {
				maxY = p.Emissions
			}
		}
	}
	collectRange(m.opsSeries)
	if hasSim {
		collectRange(m.simOpsSeries)
	}
	if !(maxY > minY) {
		if maxY <= 0 {
			maxY = 1
		}
		minY = maxY * 0.9
		maxY = maxY * 1.1
	}
	pad := (maxY - minY) * 0.25
	lo := minY - pad
	if lo < 0 {
		lo = 0
	}
	hi := maxY + pad

	unit, scale := emissionsUnit(hi)
	yLabel := func(_ int, v float64) string {
		return fmt.Sprintf("%.2f%s", v*scale, unit)
	}

	chart := timeserieslinechart.New(chartW, chartH,
		timeserieslinechart.WithTimeRange(minT, maxT),
		timeserieslinechart.WithYRange(lo, hi),
		timeserieslinechart.WithXLabelFormatter(timeserieslinechart.HourTimeLabelFormatter()),
		timeserieslinechart.WithYLabelFormatter(linechart.LabelFormatter(yLabel)),
		timeserieslinechart.WithUpdateHandler(timeserieslinechart.HourNoZoomUpdateHandler(1)),
	)
	chart.DrawXYAxisAndLabel()

	chart.SetDataSetStyle("baseline", lipglossv2.NewStyle().Foreground(lipglossv2.Color("#6272A4")))
	for _, p := range m.opsSeries {
		chart.PushDataSet("baseline", timeserieslinechart.TimePoint{Time: p.Timestamp, Value: p.Emissions})
	}

	names := []string{"baseline"}
	if hasSim {
		chart.SetDataSetStyle("optimized", lipglossv2.NewStyle().Foreground(lipglossv2.Color("#00BF72")))
		for _, p := range m.simOpsSeries {
			chart.PushDataSet("optimized", timeserieslinechart.TimePoint{Time: p.Timestamp, Value: p.Emissions})
		}
		names = append(names, "optimized")
	}
	chart.DrawDataSets(names)

	header := sectionHeadStyle.Render("📈  Operational Emissions Time Series") +
		"  " + mutedStyle.Render(fmt.Sprintf("(CO₂e %s)", strings.TrimSpace(unit)))
	if hasSim {
		legend := "  " +
			lipgloss.NewStyle().Foreground(clrBlue).Render("● current") +
			"  " +
			lipgloss.NewStyle().Foreground(clrGreen).Render("● optimized") +
			mutedStyle.Render(" · "+m.simRegion)
		header += legend
	}
	return focusedBorderStyle.
		BorderForeground(clrBlue).
		Padding(0, 1).
		Render(header + "\n\n" + chart.View())
}

// emissionsUnit chooses a display unit for kg-CO₂e values. Returns the unit
// suffix and a multiplicative scale to apply when formatting. The chart's
// internal Y range stays in kg so it composes with the underlying data.
func emissionsUnit(maxKg float64) (string, float64) {
	switch {
	case maxKg >= 1:
		return "kg", 1
	case maxKg >= 0.001:
		return "g", 1000
	default:
		return "mg", 1_000_000
	}
}

// ── Utility ───────────────────────────────────────────────────────────────────

func truncate(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if len(s) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return s[:maxLen]
	}
	return s[:maxLen-1] + "…"
}
