package main

import "time"

// toolVersion is stamped into every uploaded manifest.
const toolVersion = "carbon-optimizer 0.4.0"

// ReportManifest is the single-source-of-truth index for a stored analysis run.
// Lives at reports/<plan-id>/manifest.json.
type ReportManifest struct {
	RunID       string         `json:"run_id"`
	PlanID      string         `json:"plan_id"`
	PlanURI     string         `json:"plan_uri"`
	Region      string         `json:"region"`
	GeneratedAt time.Time      `json:"generated_at"`
	ToolVersion string         `json:"tool_version"`
	Simulation  SimulationMeta `json:"simulation"`
	Artifacts   ArtifactPaths  `json:"artifacts"`
	Totals      ReportTotals   `json:"totals"`
}

type SimulationMeta struct {
	Applied      bool   `json:"applied"`
	Optimization string `json:"optimization"`
	TargetRegion string `json:"target_region"`
}

type ArtifactPaths struct {
	Impacts      string `json:"impacts"`
	Matrix       string `json:"matrix"`
	CostBaseline string `json:"cost_baseline"`
	CostTarget   string `json:"cost_target,omitempty"`
	OpsSeries    string `json:"ops_series"`
	AIMd         string `json:"ai_md,omitempty"`
	PDF          string `json:"pdf"`
}

type ReportTotals struct {
	OpsKgDay   float64 `json:"ops_kg_day"`
	EmbKgDay   float64 `json:"emb_kg_day"`
	MonthlyUSD float64 `json:"monthly_usd"`
}

type OpsSeriesBundle struct {
	Baseline  []OpsEmissionPoint `json:"baseline"`
	Simulated []OpsEmissionPoint `json:"sim,omitempty"`
}

type RunLogEntry struct {
	RunID     string    `json:"run_id"`
	PlanID    string    `json:"plan_id"`
	Region    string    `json:"region"`
	Timestamp time.Time `json:"timestamp"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
}

// PlanSummary is shown on the S3-source screen so users can pick a prior run.
type PlanSummary struct {
	PlanID    string
	RunID     string
	Region    string
	Timestamp time.Time
	Status    string
}
