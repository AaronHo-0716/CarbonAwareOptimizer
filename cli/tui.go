package main

import (
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/lipgloss"
)

// sideW is the total width (content + borders) of the right-hand side panel.
const sideW = 36

// simCompleteMsg is sent when an async region-move simulation finishes.
type simCompleteMsg struct {
	ops          float64
	emb          float64
	total        float64
	targetRegion string
	targetCost   CostSummary
	pricingWarn  string
	impacts      []ResourceImpact
	opsSeries    []OpsEmissionPoint
}

// ── Initial model ─────────────────────────────────────────────────────────────

func initialModel() model {
	emToken, orToken := loadEnv()

	// API-key inputs (shown only when keys are missing)
	emIn := textinput.New()
	emIn.Placeholder = "Paste ElectricityMaps token…"
	emIn.Width = 50
	emIn.Focus()

	orIn := textinput.New()
	orIn.Placeholder = "Paste OpenRouter key (leave blank to skip)…"
	orIn.Width = 50

	// Side-panel inputs
	lambdaIn := textinput.New()
	lambdaIn.SetValue("50000")
	lambdaIn.CharLimit = 10
	lambdaIn.Width = sideW - 6

	workStartIn := textinput.New()
	workStartIn.SetValue("9")
	workStartIn.CharLimit = 2
	workStartIn.Width = sideW - 6

	workEndIn := textinput.New()
	workEndIn.SetValue("17")
	workEndIn.CharLimit = 2
	workEndIn.Width = sideW - 6

	workUtilIn := textinput.New()
	workUtilIn.SetValue("75")
	workUtilIn.CharLimit = 3
	workUtilIn.Width = sideW - 6

	idleUtilIn := textinput.New()
	idleUtilIn.SetValue("1")
	idleUtilIn.CharLimit = 3
	idleUtilIn.Width = sideW - 6

	regionIn := textinput.New()
	regionIn.Placeholder = "e.g. eu-west-1"
	regionIn.CharLimit = 20
	regionIn.Width = sideW - 6

	// Spinner
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#00BF72"))

	// File picker — start in cwd
	wd, _ := os.Getwd()
	fp := newFilePicker(wd)

	initState := stateFilePicker
	if emToken == "" {
		initState = stateAPIKeys
	}

	// Pre-load CSV data (fast, local disk read)
	embodied := parseEmbodiedEmissions()
	vcpus := parseInstanceVCPUs()
	x86, arm := getUseCoefficients()

	return model{
		width:  80,
		height: 24,

		state: initState,

		emInput:     emIn,
		orInput:     orIn,
		emToken:     emToken,
		orToken:     orToken,
		apiKeyFocus: 0,

		fp: fp,

		spinner: sp,
		loadMsg: "Analysing your infrastructure…",

		embodiedData: embodied,
		vcpuMap:      vcpus,
		x86Coeff:     x86,
		armCoeff:     arm,

		networkProfile:    "medium",
		lambdaInput:       lambdaIn,
		lambdaInvocations: 50000,
		workStartInput:    workStartIn,
		workEndInput:      workEndIn,
		workUtilInput:     workUtilIn,
		idleUtilInput:     idleUtilIn,
		utilization:       normalizeSchedule(UtilizationSchedule{WorkStartHour: 9, WorkEndHour: 17, WorkPct: 75, IdlePct: 1}),
		optType:           "graviton",
		regionInput:       regionIn,
		sideFocused:       sideOptGraviton,
		intensityCache:    make(map[string][]CarbonIntensityPoint),

		focus: focusMain,
	}
}

// ── .env helpers ──────────────────────────────────────────────────────────────

func loadEnv() (em, or_ string) {
	data, err := os.ReadFile(".env")
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(line, "ELECTRICITYMAPS_TOKEN="); ok {
			em = strings.TrimSpace(after)
		}
		if after, ok := strings.CutPrefix(line, "OPENROUTER_API_KEY="); ok {
			or_ = strings.TrimSpace(after)
		}
	}
	return
}

func saveEnv(emToken, orToken string) {
	content := "ELECTRICITYMAPS_TOKEN=" + emToken + "\n"
	if orToken != "" {
		content += "OPENROUTER_API_KEY=" + orToken + "\n"
	}
	_ = os.WriteFile(".env", []byte(content), 0600)
}

// ── Init ──────────────────────────────────────────────────────────────────────

func (m model) Init() tea.Cmd {
	// customFilePicker loads synchronously; only text-input blink needed.
	return textinput.Blink
}

// ── Update ────────────────────────────────────────────────────────────────────

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	// ── Resize ───────────────────────────────────────────────────────────────
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.fp.height = m.height - 10
		if m.mainVPReady {
			m.mainVP.Width = m.vpWidth()
			m.mainVP.Height = m.vpHeight()
			m.mainVP.SetContent(m.buildMainContent())
		}
		if m.sideVPReady {
			m.sideVP.Width = sideW - 2
			m.sideVP.Height = m.vpHeight()
			m.syncSideVPOffset()
		}
		return m, nil

	// ── Keys ─────────────────────────────────────────────────────────────────
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}
		switch m.state {
		case stateAPIKeys:
			return m.updateAPIKeys(msg)
		case stateFilePicker:
			switch msg.String() {
			case "q":
				return m, tea.Quit
			case "~":
				if home, err := os.UserHomeDir(); err == nil {
					m.fp.navigateTo(home)
				}
				return m, nil
			default:
				path, didSelect := m.fp.update(msg.String())
				if didSelect {
					m.selectedPath = path
					m.cachedPlan = nil
					m.errMsg = ""
					m.state = stateLoading
					m.loadMsg = "Analysing your infrastructure…"
					return m, tea.Batch(
						m.spinner.Tick,
						runAnalysisCmd(path, m.emToken, m.embodiedData, m.vcpuMap,
							m.x86Coeff, m.armCoeff, m.networkProfile, m.lambdaInvocations, m.utilization, m.cachedPlan, m.intensityCache),
					)
				}
				return m, nil
			}
		case stateLoading:
			return m, nil // absorb all keys while busy
		case stateResults:
			return m.updateResults(msg)
		}

	// ── Spinner ───────────────────────────────────────────────────────────────
	case spinner.TickMsg:
		if m.state == stateLoading {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil

	// ── Analysis complete ─────────────────────────────────────────────────────
	case analysisCompleteMsg:
		m.plan = msg.plan
		cp := msg.plan
		m.cachedPlan = &cp
		m.region = msg.region
		m.gridIntensity = msg.intensity
		m.intensityData = msg.intensityData
		m.opsSeries = msg.opsSeries
		m.impacts = msg.impacts
		m.topResource = msg.topResource
		m.totalOps = msg.totalOps
		m.totalEmb = msg.totalEmb
		m.hasNetworkRes = msg.hasNetworkRes
		m.hasLambdaRes = msg.hasLambdaRes
		m.matrixData = msg.matrixData
		m.matrixContext = msg.matrixContext
		m.baselineCost = msg.baselineCost
		m.targetCost = CostSummary{}
		m.baselineWarn = msg.pricingWarn
		m.pricingWarn = msg.pricingWarn
		m.simDone = false
		m.simImpacts = nil
		m.simOpsSeries = nil
		m.simRegion = ""
		if !msg.skipAIFetch {
			m.aiContent = ""
			m.aiLoading = true
		}
		m.state = stateResults

		// Keep latest side-panel settings so the displayed assumptions match analysis.

		// Smart default: focus on graviton option, or the first available item
		active := m.activeSideItems()
		if len(active) > 0 {
			m.sideFocused = active[0]
		} else {
			m.sideFocused = sideOptGraviton
		}

		// Initialise / resize the viewport
		vp := viewport.New(m.vpWidth(), m.vpHeight())
		vp.SetContent(m.buildMainContent())
		m.mainVP = vp
		m.mainVPReady = true

		// Side panel viewport (scrollable).
		svp := viewport.New(sideW-2, m.vpHeight())
		m.sideVP = svp
		m.sideVPReady = true
		m.syncSideVPOffset()

		if msg.skipAIFetch {
			m.aiLoading = false
			return m, nil
		}
		return m, fetchAICmd(m.orToken, msg.topResource, msg.region, msg.intensity, msg.matrixContext)

	// ── AI result ────────────────────────────────────────────────────────────
	case aiCompleteMsg:
		m.aiContent = msg.content
		m.aiContentRaw = msg.raw
		m.aiLoading = false
		if m.mainVPReady {
			m.mainVP.SetContent(m.buildMainContent())
		}
		return m, nil

	// ── PDF export result ────────────────────────────────────────────────────
	case pdfExportMsg:
		if msg.err != nil {
			m.lastExportMsg = "❌ " + msg.err.Error()
		} else {
			m.lastExportMsg = "✅ Saved " + msg.path
		}
		m.syncSideVPOffset()
		return m, nil

	// ── Region-simulation result ──────────────────────────────────────────────
	case simCompleteMsg:
		m.simOps = msg.ops
		m.simEmb = msg.emb
		m.simTotal = msg.total
		m.targetCost = msg.targetCost
		m.pricingWarn = combineWarnings(m.baselineWarn, msg.pricingWarn)
		m.simImpacts = msg.impacts
		m.simOpsSeries = msg.opsSeries
		m.simRegion = msg.targetRegion
		m.simDone = true
		if m.mainVPReady {
			m.mainVP.SetContent(m.buildMainContent())
		}
		m.syncSideVPOffset()
		return m, nil

	// ── Error ────────────────────────────────────────────────────────────────
	case errMsg:
		m.errMsg = msg.err.Error()
		m.state = stateFilePicker
		return m, nil
	}

	// ── Viewport scroll when main panel is focused ────────────────────────────
	if m.state == stateResults && m.focus == focusMain && m.mainVPReady {
		var cmd tea.Cmd
		m.mainVP, cmd = m.mainVP.Update(msg)
		return m, cmd
	}

	return m, nil
}

// ── API-keys handler ──────────────────────────────────────────────────────────

func (m model) updateAPIKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		return m, tea.Quit

	case "tab", "shift+tab":
		m.apiKeyFocus = 1 - m.apiKeyFocus
		if m.apiKeyFocus == 0 {
			m.orInput.Blur()
			cmd := m.emInput.Focus()
			return m, cmd
		}
		m.emInput.Blur()
		cmd := m.orInput.Focus()
		return m, cmd

	case "enter":
		em := strings.TrimSpace(m.emInput.Value())
		if em == "" {
			m.keyError = "ElectricityMaps token is required"
			return m, nil
		}
		m.emToken = em
		m.orToken = strings.TrimSpace(m.orInput.Value())
		saveEnv(m.emToken, m.orToken)
		m.state = stateFilePicker
		m.keyError = ""
		return m, nil
	}

	// Forward keystrokes to the focused text input
	var cmd tea.Cmd
	if m.apiKeyFocus == 0 {
		m.emInput, cmd = m.emInput.Update(msg)
	} else {
		m.orInput, cmd = m.orInput.Update(msg)
	}
	return m, cmd
}

// ── Results-state key handler ─────────────────────────────────────────────────

func (m model) updateResults(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q":
		return m, tea.Quit

	case "tab":
		if m.focus == focusMain {
			m.focus = focusSide
		} else {
			m.focus = focusMain
		}
		m, cmd := m.syncInputFocus()
		return m, cmd

	case "f":
		// Return to file picker
		m.state = stateFilePicker
		m.mainVPReady = false
		m.simDone = false
		m.simImpacts = nil
		m.simOpsSeries = nil
		m.simRegion = ""
		m.errMsg = ""
		return m, nil

	case "ctrl+p":
		m.lastExportMsg = "⏳ Exporting PDF…"
		return m, m.exportPDFCmd()
	}

	if m.focus == focusSide {
		return m.updateSidePanel(msg)
	}

	// Scroll the main viewport
	if m.mainVPReady {
		var cmd tea.Cmd
		m.mainVP, cmd = m.mainVP.Update(msg)
		return m, cmd
	}
	return m, nil
}

// ── Side-panel key handler ────────────────────────────────────────────────────

func (m model) updateSidePanel(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Text-input items intercept most keystrokes
	switch m.sideFocused {
	case sideLambdaInput, sideWorkStartHour, sideWorkEndHour, sideWorkUtilPct, sideIdleUtilPct:
		switch msg.String() {
		case "up", "k":
			m.sideFocused = m.prevSideItem()
			return m.syncInputFocus()
		case "down", "j", "tab":
			m.sideFocused = m.nextSideItem()
			return m.syncInputFocus()
		case "esc":
			m.focus = focusMain
			return m.syncInputFocus()
		default:
			return m.updateFocusedSideInput(msg)
		}

	case sideRegionInput:
		switch msg.String() {
		case "up", "k":
			m.sideFocused = m.prevSideItem()
			return m.syncInputFocus()
		case "down", "j", "tab":
			m.sideFocused = m.nextSideItem()
			return m.syncInputFocus()
		case "esc":
			m.focus = focusMain
			return m.syncInputFocus()
		default:
			var cmd tea.Cmd
			m.regionInput, cmd = m.regionInput.Update(msg)
			return m, cmd
		}
	}

	// Manual scrolling of the side panel viewport (doesn't change focus).
	switch msg.String() {
	case "pgup", "ctrl+u":
		m.sideVP.LineUp(m.sideVP.Height / 2)
		return m, nil
	case "pgdown", "ctrl+d":
		m.sideVP.LineDown(m.sideVP.Height / 2)
		return m, nil
	}

	// General navigation
	switch msg.String() {
	case "up", "k":
		m.sideFocused = m.prevSideItem()
		return m.syncInputFocus()

	case "down", "j", "tab":
		m.sideFocused = m.nextSideItem()
		return m.syncInputFocus()

	case "left", "h":
		if m.sideFocused == sideNetworkProfile {
			m.networkProfile = prevProfile(m.networkProfile)
		}

	case "right", "l":
		if m.sideFocused == sideNetworkProfile {
			m.networkProfile = nextProfile(m.networkProfile)
		}

	case "enter", " ":
		switch m.sideFocused {
		case sideNetworkProfile:
			m.networkProfile = nextProfile(m.networkProfile)
		case sideOptGraviton:
			m.optType = "graviton"
			m.clampSideItem()
		case sideOptRegion:
			m.optType = "region"
			m.clampSideItem()
			return m.syncInputFocus()
		case sideReanalyze:
			return m.triggerReanalysis()
		case sideApply:
			return m.applyOptimization()
		case sideExportPDF:
			m.lastExportMsg = "⏳ Exporting PDF…"
			return m, m.exportPDFCmd()
		}

	case "esc":
		m.focus = focusMain
		return m.syncInputFocus()
	}

	return m, nil
}

func (m model) updateFocusedSideInput(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	switch m.sideFocused {
	case sideLambdaInput:
		m.lambdaInput, cmd = m.lambdaInput.Update(msg)
	case sideWorkStartHour:
		m.workStartInput, cmd = m.workStartInput.Update(msg)
	case sideWorkEndHour:
		m.workEndInput, cmd = m.workEndInput.Update(msg)
	case sideWorkUtilPct:
		m.workUtilInput, cmd = m.workUtilInput.Update(msg)
	case sideIdleUtilPct:
		m.idleUtilInput, cmd = m.idleUtilInput.Update(msg)
	}
	return m, cmd
}

// ── Side-panel helpers ────────────────────────────────────────────────────────

// activeSideItems returns the list of interactive elements currently shown,
// filtering out options that don't apply to the loaded plan.
func (m model) activeSideItems() []sideItem {
	var items []sideItem
	if m.hasNetworkRes {
		items = append(items, sideNetworkProfile)
	}
	if m.hasLambdaRes {
		items = append(items, sideLambdaInput)
	}
	items = append(items,
		sideWorkStartHour,
		sideWorkEndHour,
		sideWorkUtilPct,
		sideIdleUtilPct,
		sideReanalyze,
	)
	items = append(items, sideOptGraviton, sideOptRegion)
	if m.optType == "region" {
		items = append(items, sideRegionInput)
	}
	items = append(items, sideApply, sideExportPDF)
	return items
}

// clampSideItem ensures sideFocused still exists in the active list;
// resets to first item if not.
func (m *model) clampSideItem() {
	for _, it := range m.activeSideItems() {
		if it == m.sideFocused {
			return
		}
	}
	active := m.activeSideItems()
	if len(active) > 0 {
		m.sideFocused = active[0]
	}
}

func (m model) nextSideItem() sideItem {
	active := m.activeSideItems()
	for i, it := range active {
		if it == m.sideFocused && i < len(active)-1 {
			return active[i+1]
		}
	}
	return m.sideFocused
}

func (m model) prevSideItem() sideItem {
	active := m.activeSideItems()
	for i, it := range active {
		if it == m.sideFocused && i > 0 {
			return active[i-1]
		}
	}
	return m.sideFocused
}

// syncSideVPOffset recomputes the side viewport scroll offset so the focused
// item stays visible. Call this whenever the focused side item changes or the
// panel content's height changes (e.g. after simulation completes).
func (m *model) syncSideVPOffset() {
	if m.sideVP.Height <= 0 {
		return
	}
	body := m.renderSidePanelBody()
	m.sideVP.SetYOffset(sideVPOffsetForFocus(body, m.sideVP.YOffset, m.sideVP.Height))
}

// syncInputFocus blurs all text inputs then focuses the one matching sideFocused.
// Returns an updated model copy and the optional blink command from Focus().
func (m model) syncInputFocus() (model, tea.Cmd) {
	m.lambdaInput.Blur()
	m.workStartInput.Blur()
	m.workEndInput.Blur()
	m.workUtilInput.Blur()
	m.idleUtilInput.Blur()
	m.regionInput.Blur()
	m.syncSideVPOffset()
	if m.focus == focusSide {
		switch m.sideFocused {
		case sideLambdaInput:
			cmd := m.lambdaInput.Focus()
			return m, cmd
		case sideWorkStartHour:
			cmd := m.workStartInput.Focus()
			return m, cmd
		case sideWorkEndHour:
			cmd := m.workEndInput.Focus()
			return m, cmd
		case sideWorkUtilPct:
			cmd := m.workUtilInput.Focus()
			return m, cmd
		case sideIdleUtilPct:
			cmd := m.idleUtilInput.Focus()
			return m, cmd
		case sideRegionInput:
			cmd := m.regionInput.Focus()
			return m, cmd
		}
	}
	return m, nil
}

func nextProfile(p string) string {
	switch p {
	case "low":
		return "medium"
	case "medium":
		return "high"
	default:
		return "low"
	}
}

func prevProfile(p string) string {
	switch p {
	case "high":
		return "medium"
	case "medium":
		return "low"
	default:
		return "high"
	}
}

// ── Re-analysis ───────────────────────────────────────────────────────────────

func (m model) triggerReanalysis() (model, tea.Cmd) {
	inv, err := strconv.Atoi(strings.TrimSpace(m.lambdaInput.Value()))
	if err != nil || inv < 0 {
		inv = 50000
	}
	m.lambdaInvocations = inv
	m.utilization = parseUtilizationScheduleFromInputs(
		m.workStartInput.Value(),
		m.workEndInput.Value(),
		m.workUtilInput.Value(),
		m.idleUtilInput.Value(),
	)
	m.workStartInput.SetValue(strconv.Itoa(m.utilization.WorkStartHour))
	m.workEndInput.SetValue(strconv.Itoa(m.utilization.WorkEndHour))
	m.workUtilInput.SetValue(strconv.Itoa(int(m.utilization.WorkPct)))
	m.idleUtilInput.SetValue(strconv.Itoa(int(m.utilization.IdlePct)))
	m.state = stateLoading
	m.loadMsg = "Re-analysing with updated settings…"
	m.mainVPReady = false
	m.simDone = false
	m.simImpacts = nil
	m.simOpsSeries = nil
	m.simRegion = ""
	if m.cachedPlan == nil {
		return m, tea.Batch(
			m.spinner.Tick,
			runAnalysisCmd(m.selectedPath, m.emToken, m.embodiedData, m.vcpuMap,
				m.x86Coeff, m.armCoeff, m.networkProfile, m.lambdaInvocations, m.utilization, m.cachedPlan, m.intensityCache),
		)
	}
	return m, tea.Batch(
		m.spinner.Tick,
		runReanalysisCmd(
			*m.cachedPlan, m.region, m.intensityData, m.matrixData, m.emToken,
			m.embodiedData, m.vcpuMap, m.x86Coeff, m.armCoeff, m.networkProfile, m.lambdaInvocations, m.utilization,
		),
	)
}

// ── Optimisation simulation ───────────────────────────────────────────────────

func (m model) applyOptimization() (model, tea.Cmd) {
	m.utilization = parseUtilizationScheduleFromInputs(
		m.workStartInput.Value(),
		m.workEndInput.Value(),
		m.workUtilInput.Value(),
		m.idleUtilInput.Value(),
	)
	m.workStartInput.SetValue(strconv.Itoa(m.utilization.WorkStartHour))
	m.workEndInput.SetValue(strconv.Itoa(m.utilization.WorkEndHour))
	m.workUtilInput.SetValue(strconv.Itoa(int(m.utilization.WorkPct)))
	m.idleUtilInput.SetValue(strconv.Itoa(int(m.utilization.IdlePct)))

	if m.optType == "graviton" {
		// Pure local computation — no HTTP needed
		simPlan := simulateGraviton(m.plan)
		simImpacts, simOps, simEmb, simOpsSeries := calculateImpactWithSeries(
			&simPlan, m.region, m.intensityData,
			m.embodiedData, m.vcpuMap, m.x86Coeff, m.armCoeff,
			m.networkProfile, m.lambdaInvocations, m.utilization,
		)
		m.simOps = simOps
		m.simEmb = simEmb
		m.simTotal = simOps + simEmb
		m.simImpacts = simImpacts
		m.simOpsSeries = simOpsSeries
		m.simRegion = m.region
		targetCost, warn := buildCostSummary(&simPlan, m.region, m.lambdaInvocations)
		m.targetCost = targetCost
		m.pricingWarn = combineWarnings(m.baselineWarn, warn)
		m.simDone = true
		if m.mainVPReady {
			m.mainVP.SetContent(m.buildMainContent())
		}
		return m, nil
	}

	// Region move: fetch new intensity async to avoid blocking the UI
	newRegion := strings.TrimSpace(m.regionInput.Value())
	if newRegion == "" {
		newRegion = m.region
	}
	return m, runRegionSimCmd(
		newRegion, m.emToken, m.plan,
		m.embodiedData, m.vcpuMap, m.x86Coeff, m.armCoeff,
		m.networkProfile, m.lambdaInvocations, m.utilization, m.intensityCache,
	)
}

func combineWarnings(base, sim string) string {
	base = strings.TrimSpace(base)
	sim = strings.TrimSpace(sim)
	switch {
	case base == "" && sim == "":
		return ""
	case base == "":
		return sim
	case sim == "":
		return base
	case base == sim:
		return base
	default:
		return base + "\n" + sim
	}
}

func parseUtilizationScheduleFromInputs(startS, endS, workPctS, idlePctS string) UtilizationSchedule {
	s := UtilizationSchedule{WorkStartHour: 0, WorkEndHour: 24, WorkPct: 50, IdlePct: 50}
	if v, err := strconv.Atoi(strings.TrimSpace(startS)); err == nil {
		s.WorkStartHour = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(endS)); err == nil {
		s.WorkEndHour = v
	}
	if v, err := strconv.Atoi(strings.TrimSpace(workPctS)); err == nil {
		s.WorkPct = float64(v)
	}
	if v, err := strconv.Atoi(strings.TrimSpace(idlePctS)); err == nil {
		s.IdlePct = float64(v)
	}
	return normalizeSchedule(s)
}

// ── Layout helpers ────────────────────────────────────────────────────────────

// vpWidth is the inner content width of the main scrollable viewport.
func (m model) vpWidth() int {
	w := m.width - sideW - 2 // 2 = left border + gap
	if w < 40 {
		w = 40
	}
	return w
}

// vpHeight is the inner content height of the main scrollable viewport.
func (m model) vpHeight() int {
	h := m.height - 3 // title bar (1) + help bar (1) + viewport border (1)
	if h < 10 {
		h = 10
	}
	return h
}

// ── Tea commands ──────────────────────────────────────────────────────────────

func runAnalysisCmd(
	path, emToken string,
	embodied map[string]float64, vcpuMap map[string]int,
	x86, arm UseCoeff,
	networkProfile string, lambdaInv int,
	schedule UtilizationSchedule,
	cachedPlan *TFPlan,
	intensityCache map[string][]CarbonIntensityPoint,
) tea.Cmd {
	return func() tea.Msg {
		var plan TFPlan
		if cachedPlan != nil {
			plan = *cachedPlan
		} else {
			loaded, err := loadPlan(path)
			if err != nil {
				return errMsg{err}
			}
			plan = loaded
		}
		region := extractRegion(plan)
		intensityData := intensityCache[region]
		if len(intensityData) == 0 {
			end := time.Now().UTC()
			start := end.Add(-24 * time.Hour)
			intensityData = getGridIntensitySeriesQuiet(emToken, region, start, end)
			if len(intensityData) > 0 {
				intensityCache[region] = intensityData
			}
		}
		intensity := weightedAverageIntensity(intensityData)
		impacts, totalOps, totalEmb, opsSeries := calculateImpactWithSeries(
			&plan, region, intensityData, embodied, vcpuMap, x86, arm, networkProfile, lambdaInv, schedule,
		)
		baselineCost, pricingWarn := buildCostSummary(&plan, region, lambdaInv)

		var topResource ResourceImpact
		highest := -1.0
		for _, imp := range impacts {
			if imp.TotalDaily > highest {
				highest = imp.TotalDaily
				topResource = imp
			}
		}

		hasNetwork, hasLambda := detectResourceTypes(plan)
		matrixData, matrixCtx := buildRegionalMatrix(
			emToken, region, intensity, totalOps,
			plan, embodied, vcpuMap, x86, arm, networkProfile, lambdaInv, schedule,
		)

		return analysisCompleteMsg{
			plan:          plan,
			region:        region,
			intensity:     intensity,
			intensityData: intensityData,
			opsSeries:     opsSeries,
			impacts:       impacts,
			topResource:   topResource,
			totalOps:      totalOps,
			totalEmb:      totalEmb,
			hasNetworkRes: hasNetwork,
			hasLambdaRes:  hasLambda,
			matrixData:    matrixData,
			matrixContext: matrixCtx,
			baselineCost:  baselineCost,
			pricingWarn:   pricingWarn,
		}
	}
}

func runReanalysisCmd(
	plan TFPlan,
	region string,
	intensityData []CarbonIntensityPoint,
	matrixData []MatrixRow,
	emToken string,
	embodied map[string]float64, vcpuMap map[string]int,
	x86, arm UseCoeff,
	networkProfile string, lambdaInv int,
	schedule UtilizationSchedule,
) tea.Cmd {
	return func() tea.Msg {
		if len(intensityData) == 0 {
			end := time.Now().UTC()
			start := end.Add(-24 * time.Hour)
			intensityData = getGridIntensitySeriesQuiet(emToken, region, start, end)
		}
		intensity := weightedAverageIntensity(intensityData)
		impacts, totalOps, totalEmb, opsSeries := calculateImpactWithSeries(
			&plan, region, intensityData, embodied, vcpuMap, x86, arm, networkProfile, lambdaInv, schedule,
		)
		baselineCost, pricingWarn := buildCostSummary(&plan, region, lambdaInv)

		var topResource ResourceImpact
		highest := -1.0
		for _, imp := range impacts {
			if imp.TotalDaily > highest {
				highest = imp.TotalDaily
				topResource = imp
			}
		}

		updatedMatrix := make([]MatrixRow, 0, len(matrixData))
		for _, row := range matrixData {
			targetRegion := strings.Fields(row.RegionName)[0]
			_, simOps, simEmb := calculateImpact(
				&plan, targetRegion, row.Intensity, embodied, vcpuMap, x86, arm, networkProfile, lambdaInv, schedule,
			)
			costSummary, _ := buildCostSummary(&plan, targetRegion, lambdaInv)
			deltaStr := "—"
			if targetRegion != region && totalOps > 0 {
				pct := ((simOps - totalOps) / totalOps) * 100
				if pct >= 0 {
					deltaStr = "+" + strconv.FormatFloat(pct, 'f', 1, 64) + "%"
				} else {
					deltaStr = strconv.FormatFloat(pct, 'f', 1, 64) + "%"
				}
			}
			row.Total = simOps + simEmb
			row.DeltaStr = deltaStr
			row.HourlyCost = costSummary.TotalHourly
			row.MonthlyCost = costSummary.TotalMonthly
			row.CostKnown = len(costSummary.Resources) > 0 && costSummary.UnavailableCount < len(costSummary.Resources)
			updatedMatrix = append(updatedMatrix, row)
		}
		matrixCtx := "Live Regional Trade-off Matrix:\n(recomputed from cached intensity and current side-panel settings)\n"

		hasNetwork, hasLambda := detectResourceTypes(plan)
		return analysisCompleteMsg{
			plan:          plan,
			region:        region,
			intensity:     intensity,
			intensityData: intensityData,
			opsSeries:     opsSeries,
			skipAIFetch:   true,
			impacts:       impacts,
			topResource:   topResource,
			totalOps:      totalOps,
			totalEmb:      totalEmb,
			hasNetworkRes: hasNetwork,
			hasLambdaRes:  hasLambda,
			matrixData:    updatedMatrix,
			matrixContext: matrixCtx,
			baselineCost:  baselineCost,
			pricingWarn:   pricingWarn,
		}
	}
}

func fetchAICmd(orToken string, top ResourceImpact, region string, intensity float64, matrixCtx string) tea.Cmd {
	return func() tea.Msg {
		raw := getAISuggestions(orToken, top, region, intensity, matrixCtx)
		rendered, err := glamour.Render(raw, "dark")
		if err != nil {
			rendered = raw
		}
		return aiCompleteMsg{content: rendered, raw: raw}
	}
}

func runRegionSimCmd(
	newRegion, emToken string,
	plan TFPlan,
	embodied map[string]float64, vcpuMap map[string]int,
	x86, arm UseCoeff,
	networkProfile string, lambdaInv int,
	schedule UtilizationSchedule,
	intensityCache map[string][]CarbonIntensityPoint,
) tea.Cmd {
	return func() tea.Msg {
		intensityData := intensityCache[newRegion]
		if len(intensityData) == 0 {
			end := time.Now().UTC()
			start := end.Add(-24 * time.Hour)
			intensityData = getGridIntensitySeriesQuiet(emToken, newRegion, start, end)
			if len(intensityData) > 0 {
				intensityCache[newRegion] = intensityData
			}
		}
		simImpacts, simOps, simEmb, simOpsSeries := calculateImpactWithSeries(
			&plan, newRegion, intensityData, embodied, vcpuMap, x86, arm, networkProfile, lambdaInv, schedule,
		)
		targetCost, pricingWarn := buildCostSummary(&plan, newRegion, lambdaInv)
		return simCompleteMsg{
			ops:          simOps,
			emb:          simEmb,
			total:        simOps + simEmb,
			targetRegion: newRegion,
			targetCost:   targetCost,
			pricingWarn:  pricingWarn,
			impacts:      simImpacts,
			opsSeries:    simOpsSeries,
		}
	}
}
