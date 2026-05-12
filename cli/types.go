package main

import (
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
)

// ── Terraform plan JSON types ─────────────────────────────────────────────────

type TFPlan struct {
	Variables map[string]struct {
		Value interface{} `json:"value"`
	} `json:"variables"`
	PlannedValues struct {
		RootModule struct {
			Resources []struct {
				Address      string                 `json:"address"`
				Mode         string                 `json:"mode"`
				Type         string                 `json:"type"`
				Name         string                 `json:"name"`
				ProviderName string                 `json:"provider_name"`
				Values       map[string]interface{} `json:"values"`
			} `json:"resources"`
		} `json:"root_module"`
	} `json:"planned_values"`
	Configuration struct {
		ProviderConfig map[string]struct {
			Expressions map[string]struct {
				ConstantValue string `json:"constant_value"`
			} `json:"expressions"`
		} `json:"provider_config"`
		RootModule struct {
			Resources []struct {
				Address     string                 `json:"address"`
				Type        string                 `json:"type"`
				Name        string                 `json:"name"`
				Expressions map[string]interface{} `json:"expressions"`
			} `json:"resources"`
		} `json:"root_module"`
	} `json:"configuration"`
}

type UseCoeff struct {
	MinWatts float64
	MaxWatts float64
}

type UtilizationSchedule struct {
	WorkStartHour int
	WorkEndHour   int
	WorkPct       float64
	IdlePct       float64
}

type ResourceImpact struct {
	Type       string
	Name       string
	Instance   string
	Count      int
	DailyOps   float64
	DailyEmb   float64
	TotalDaily float64
}

type CarbonIntensityResponse struct {
	CarbonIntensity float64 `json:"carbonIntensity"`
}

type RegionIntensity struct {
	Region    string
	Intensity float64
}

type CarbonIntensityPoint struct {
	Timestamp time.Time
	Intensity float64
}

type OpsEmissionPoint struct {
	Timestamp time.Time
	Emissions float64
}

type AIRequest struct {
	Model    string `json:"model"`
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
}

type AIResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// MatrixRow holds a single row of regional comparison data.
type MatrixRow struct {
	RegionName  string
	Intensity   float64
	Total       float64
	DeltaStr    string
	HourlyCost  float64
	MonthlyCost float64
	CostKnown   bool
}

type CostResource struct {
	Address   string
	Type      string
	Name      string
	Quantity  float64
	Unit      string
	Hourly    float64
	Monthly   float64
	Available bool
	Note      string
}

type CostSummary struct {
	Region           string
	Resources        []CostResource
	TotalHourly      float64
	TotalMonthly     float64
	UnavailableCount int
}

// ── TUI state types ───────────────────────────────────────────────────────────

type appState int

const (
	stateAPIKeys    appState = iota
	stateFilePicker          // file-picker to choose plan
	stateLoading             // running analysis
	stateResults             // showing results + side panel
)

type panelFocus int

const (
	focusMain panelFocus = iota
	focusSide
)

// sideItem identifies each interactive element in the side panel.
type sideItem int

const (
	sideNetworkProfile sideItem = iota
	sideLambdaInput
	sideWorkStartHour
	sideWorkEndHour
	sideWorkUtilPct
	sideIdleUtilPct
	sideReanalyze
	sideOptGraviton
	sideOptRegion
	sideRegionInput
	sideApply
	sideExportPDF
)

// ── Bubble Tea messages ───────────────────────────────────────────────────────

type errMsg struct{ err error }

type analysisCompleteMsg struct {
	plan          TFPlan
	region        string
	intensity     float64
	intensityData []CarbonIntensityPoint
	opsSeries     []OpsEmissionPoint
	skipAIFetch   bool
	impacts       []ResourceImpact
	topResource   ResourceImpact
	totalOps      float64
	totalEmb      float64
	hasNetworkRes bool
	hasLambdaRes  bool
	matrixData    []MatrixRow
	matrixContext string
	baselineCost  CostSummary
	pricingWarn   string
}

type aiCompleteMsg struct {
	content string // glamour-rendered (ANSI) string for the TUI
	raw     string // original markdown source (kept for PDF export)
}

// ── TUI model ─────────────────────────────────────────────────────────────────

type model struct {
	// Terminal dimensions
	width  int
	height int

	// App state
	state appState

	// API Keys popup
	emInput     textinput.Model
	orInput     textinput.Model
	apiKeyFocus int // 0 = EM token, 1 = OpenRouter key
	emToken     string
	orToken     string
	keyError    string

	// File picker
	fp           customFilePicker
	selectedPath string
	cachedPlan   *TFPlan

	// Loading spinner
	spinner spinner.Model
	loadMsg string

	// Preloaded coefficient data (parsed once at startup)
	embodiedData map[string]float64
	vcpuMap      map[string]int
	x86Coeff     UseCoeff
	armCoeff     UseCoeff

	// Analysis results
	plan          TFPlan
	region        string
	gridIntensity float64
	intensityData []CarbonIntensityPoint
	opsSeries     []OpsEmissionPoint
	impacts       []ResourceImpact
	topResource   ResourceImpact
	totalOps      float64
	totalEmb      float64
	hasNetworkRes bool
	hasLambdaRes  bool

	// Regional matrix data (raw, rendered into table at paint time)
	matrixData    []MatrixRow
	matrixContext string
	baselineCost  CostSummary
	targetCost    CostSummary
	baselineWarn  string
	pricingWarn   string

	// AI output (glamour-rendered markdown string + raw markdown source)
	aiContent    string
	aiContentRaw string
	aiLoading    bool

	// Transient status line shown in side panel after PDF export.
	lastExportMsg string

	// Main scrollable viewport (left panel)
	mainVP      viewport.Model
	mainVPReady bool

	// Side-panel scrollable viewport (right panel)
	sideVP      viewport.Model
	sideVPReady bool

	// Panel focus
	focus panelFocus

	// Side panel state
	networkProfile    string // "low" | "medium" | "high"
	lambdaInput       textinput.Model
	lambdaInvocations int
	workStartInput    textinput.Model
	workEndInput      textinput.Model
	workUtilInput     textinput.Model
	idleUtilInput     textinput.Model
	utilization       UtilizationSchedule
	sideFocused       sideItem // currently focused side-panel element
	optType           string   // "graviton" | "region"
	regionInput       textinput.Model
	intensityCache    map[string][]CarbonIntensityPoint

	// Simulation result (after clicking Apply)
	simOps       float64
	simEmb       float64
	simTotal     float64
	simDone      bool
	simImpacts   []ResourceImpact
	simOpsSeries []OpsEmissionPoint
	simRegion    string

	// General error text (shown in file-picker view)
	errMsg string
}
