package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/table"
	"github.com/charmbracelet/lipgloss"
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
	case stateFilePicker:
		return m.viewFilePicker()
	case stateLoading:
		return m.viewLoading()
	case stateResults:
		return m.viewResults()
	}
	return ""
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

	// ── Settings section (only if relevant resources exist) ───────────────────
	if m.hasNetworkRes || m.hasLambdaRes {
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

		if isActive(sideReanalyze) {
			b.WriteString(cursor(sideReanalyze) + btn("  Re-analyse  ", sideReanalyze) + "\n\n")
		}

		b.WriteString(dimStyle.Render(strings.Repeat("─", sideW-4)) + "\n\n")
	}

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

	// ── Border + dimensions ───────────────────────────────────────────────────
	borderStyle := unfocusedBorderStyle
	if isFocused {
		borderStyle = focusedBorderStyle
	}
	return borderStyle.
		Width(sideW - 2). // -2 for the two border chars
		Height(m.vpHeight()).
		Render(b.String())
}

// ── Main viewport content ─────────────────────────────────────────────────────

func (m model) buildMainContent() string {
	parts := []string{
		m.buildImpactTable(),
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
	// Overhead: box border(2) + box padding(2) + 7 cols × cell padding(2) = 18
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
