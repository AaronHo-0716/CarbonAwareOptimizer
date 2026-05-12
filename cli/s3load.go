package main

import (
	"context"
	"encoding/json"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// s3LoadCmd downloads every artifact for a stored plan and returns them as a
// reportBundle. The Update loop handles the message and decants the bundle
// into the model.
func s3LoadCmd(client *s3Client, bucket, planID string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return s3LoadCompleteMsg{planID: planID, err: errFromString("AWS client not initialised")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		bundle, err := client.getReport(ctx, bucket, planID)
		return s3LoadCompleteMsg{planID: planID, bundle: bundle, err: err}
	}
}

// applyS3Bundle mutates the model to display a previously-stored run. The
// caller is responsible for transitioning to stateResults and initialising
// the viewports.
//
// A stored bundle contains only one impacts.json (either baseline or sim),
// so on reload we always render it as a single snapshot. The Simulation
// metadata is preserved on the model so the status line can mention what
// optimization was being explored.
func applyS3Bundle(m *model, bundle reportBundle) {
	mf := bundle.Manifest
	m.region = mf.Region
	m.impacts = bundle.Impacts
	m.matrixData = bundle.Matrix
	m.baselineCost = bundle.CostBaseline
	m.opsSeries = bundle.OpsSeries.Baseline
	m.totalOps = mf.Totals.OpsKgDay
	m.totalEmb = mf.Totals.EmbKgDay
	m.aiContentRaw = bundle.AIMd
	m.aiContent = bundle.AIMd
	m.loadedFromS3 = true
	m.pricingWarn = ""
	m.baselineWarn = ""
	m.targetCost = bundle.CostTarget
	m.optType = mf.Simulation.Optimization
	m.simRegion = mf.Simulation.TargetRegion

	// We don't have a separate pre-sim baseline in the bundle, so don't show
	// the side-by-side comparison view.
	m.simDone = false
	m.simOps = 0
	m.simEmb = 0
	m.simTotal = 0
	m.simImpacts = nil
	m.simOpsSeries = nil

	// Pick a top resource by total daily emissions (used for AI prompt context
	// even though we won't re-fetch AI for an S3 load).
	var top ResourceImpact
	highest := -1.0
	for _, imp := range bundle.Impacts {
		if imp.TotalDaily > highest {
			highest = imp.TotalDaily
			top = imp
		}
	}
	m.topResource = top

	// If we have the redacted plan JSON, parse it so re-simulation still works.
	if len(bundle.PlanJSON) > 0 {
		var plan TFPlan
		if err := json.Unmarshal(bundle.PlanJSON, &plan); err == nil {
			m.plan = plan
			cp := plan
			m.cachedPlan = &cp
			m.hasNetworkRes, m.hasLambdaRes = detectResourceTypes(plan)
		}
		m.rawPlanBytes = bundle.PlanJSON
	}
	m.selectedPath = "s3://" + m.s3Bucket + "/plans/" + mf.PlanID + ".json"
}

// errFromString is a tiny helper to keep s3LoadCmd self-contained without
// importing fmt.
type stringErr string

func (s stringErr) Error() string { return string(s) }

func errFromString(s string) error { return stringErr(s) }
