package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// slugRe is enforced on the user-supplied plan-id so the S3 key path stays sane.
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// updateS3Slug runs while the slug prompt is on screen (entered via Ctrl+S from
// the results view). Enter submits; if no bucket exists yet we route through
// the create-bucket screen first.
func (m model) updateS3Slug(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.state = stateResults
		m.s3Error = ""
		return m, nil
	case "enter":
		slug := strings.TrimSpace(strings.ToLower(m.s3SlugInput.Value()))
		if !slugRe.MatchString(slug) {
			m.s3Error = "plan-id must match [a-z0-9-], 1-63 chars, start with [a-z0-9]"
			return m, nil
		}
		m.s3Error = ""
		m.s3SlugInput.SetValue(slug)
		if m.s3Bucket == "" {
			// Need to create the bucket first.
			m.s3CreateInput.SetValue(m.defaultBucketName())
			m.s3CreateInput.Focus()
			m.state = stateS3CreateBucket
			return m, nil
		}
		m.state = stateResults
		m.lastUploadMsg = "⏳ Uploading to S3…"
		return m, s3UploadCmd(m, slug)
	}
	var cmd tea.Cmd
	m.s3SlugInput, cmd = m.s3SlugInput.Update(msg)
	return m, cmd
}

// updateS3CreateBucket runs while the create-bucket prompt is on screen.
func (m model) updateS3CreateBucket(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.state = stateResults
		m.s3Error = ""
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.s3CreateInput.Value())
		if name == "" {
			m.s3Error = "bucket name required"
			return m, nil
		}
		m.s3Error = ""
		m.state = stateResults
		m.lastUploadMsg = fmt.Sprintf("⏳ Creating bucket %s…", name)
		return m, s3CreateBucketCmd(m.s3, name)
	}
	var cmd tea.Cmd
	m.s3CreateInput, cmd = m.s3CreateInput.Update(msg)
	return m, cmd
}

// defaultBucketName returns a sensible default for the create prompt.
func (m model) defaultBucketName() string {
	if m.s3 == nil {
		return "carbon-optimizer-bucket"
	}
	return m.s3.defaultBucketName()
}

// s3CreateBucketCmd creates and tags the bucket, then signals success.
func s3CreateBucketCmd(client *s3Client, name string) tea.Cmd {
	return func() tea.Msg {
		if client == nil {
			return s3CreateBucketMsg{name: name, err: fmt.Errorf("AWS client not initialised")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := client.createBucket(ctx, name); err != nil {
			return s3CreateBucketMsg{name: name, err: err}
		}
		return s3CreateBucketMsg{name: name}
	}
}

// s3UploadCmd performs the full report upload. Designed to be called after the
// model has captured the slug; the slug is read off m.s3SlugInput.
func s3UploadCmd(m model, planID string) tea.Cmd {
	// Snapshot everything we need from the model so the closure doesn't capture
	// the live struct (Bubble Tea would otherwise see a torn read).
	client := m.s3
	bucket := m.s3Bucket
	rawPlan := append([]byte(nil), m.rawPlanBytes...)
	region := m.region
	simulationApplied := m.simDone
	optType := m.optType
	simRegion := m.simRegion
	totals := ReportTotals{
		OpsKgDay:   m.totalOps,
		EmbKgDay:   m.totalEmb,
		MonthlyUSD: m.baselineCost.TotalMonthly,
	}
	if simulationApplied {
		totals = ReportTotals{
			OpsKgDay:   m.simOps,
			EmbKgDay:   m.simEmb,
			MonthlyUSD: m.targetCost.TotalMonthly,
		}
	}
	bundle := reportBundle{
		Impacts:      append([]ResourceImpact(nil), m.impacts...),
		Matrix:       append([]MatrixRow(nil), m.matrixData...),
		CostBaseline: m.baselineCost,
		CostTarget:   m.targetCost,
		OpsSeries: OpsSeriesBundle{
			Baseline:  append([]OpsEmissionPoint(nil), m.opsSeries...),
			Simulated: append([]OpsEmissionPoint(nil), m.simOpsSeries...),
		},
		AIMd: m.aiContentRaw,
	}
	if simulationApplied && len(m.simImpacts) > 0 {
		bundle.Impacts = append([]ResourceImpact(nil), m.simImpacts...)
	}
	pdfBytes, pdfErr := buildReportPDFBytes(m)

	return func() tea.Msg {
		if client == nil || bucket == "" {
			return s3UploadMsg{planID: planID, err: fmt.Errorf("S3 client/bucket not initialised")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		// 1. Redact + upload the plan JSON.
		if len(rawPlan) > 0 {
			redacted, err := redactPlanBytes(rawPlan)
			if err != nil {
				return s3UploadMsg{planID: planID, err: fmt.Errorf("redact plan: %w", err)}
			}
			if err := client.putPlanJSON(ctx, bucket, planID, redacted); err != nil {
				return s3UploadMsg{planID: planID, err: fmt.Errorf("upload plan: %w", err)}
			}
		}

		// 2. Build the manifest. PlanURI links the report back to its plan.
		runID := newRunID(time.Now().UTC())
		now := time.Now().UTC()
		artifacts := ArtifactPaths{
			Impacts:      "impacts.json",
			Matrix:       "matrix.json",
			CostBaseline: "cost-baseline.json",
			OpsSeries:    "ops-series.json",
			PDF:          "report.pdf",
		}
		if simulationApplied {
			artifacts.CostTarget = "cost-target.json"
		}
		if bundle.AIMd != "" {
			artifacts.AIMd = "ai-recommendation.md"
		}
		bundle.Manifest = ReportManifest{
			RunID:       runID,
			PlanID:      planID,
			PlanURI:     fmt.Sprintf("s3://%s/plans/%s.json", bucket, planID),
			Region:      region,
			GeneratedAt: now,
			ToolVersion: toolVersion,
			Simulation: SimulationMeta{
				Applied:      simulationApplied,
				Optimization: optType,
				TargetRegion: simRegion,
			},
			Artifacts: artifacts,
			Totals:    totals,
		}

		// 3. Upload all JSON + markdown artifacts.
		if err := client.putReportBundle(ctx, bucket, planID, bundle); err != nil {
			return s3UploadMsg{planID: planID, err: err}
		}

		// 4. Upload the PDF if it built successfully.
		if pdfErr == nil && len(pdfBytes) > 0 {
			if err := client.putPDF(ctx, bucket, planID, pdfBytes); err != nil {
				return s3UploadMsg{planID: planID, err: fmt.Errorf("upload pdf: %w", err)}
			}
		}

		// 5. Append a run-log line. Best-effort: failures shouldn't undo the upload.
		_ = client.appendRunLog(ctx, bucket, RunLogEntry{
			RunID:     runID,
			PlanID:    planID,
			Region:    region,
			Timestamp: now,
			Status:    "ok",
		})

		return s3UploadMsg{planID: planID}
	}
}

// viewS3Slug renders the slug-entry overlay.
func (m model) viewS3Slug() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BF72")).
		Render("Save analysis to S3")
	b.WriteString(title + "\n\n")
	b.WriteString("Enter a plan-id slug. This is the join key between the\n")
	b.WriteString("plan JSON and its analysis reports inside the bucket.\n\n")
	b.WriteString("  Plan-id: ")
	b.WriteString(m.s3SlugInput.View())
	b.WriteString("\n\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render("Format: [a-z0-9-], 1-63 chars, start with [a-z0-9]"))
	b.WriteString("\n")
	if m.s3Error != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("⚠ " + m.s3Error))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render("enter save · esc cancel"))
	return b.String()
}

// viewS3CreateBucket renders the bucket-name confirmation overlay.
func (m model) viewS3CreateBucket() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BF72")).
		Render("Create Carbon-Optimizer bucket")
	b.WriteString(title + "\n\n")
	b.WriteString("No Carbon-Optimizer-tagged bucket was found. A new one will\n")
	b.WriteString("be created in your default region and tagged Carbon-Optimizer=true.\n\n")
	b.WriteString("  Bucket name: ")
	b.WriteString(m.s3CreateInput.View())
	b.WriteString("\n")
	if m.s3 != nil {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
			Render(fmt.Sprintf("  Region:      %s", m.s3.region)))
		b.WriteString("\n")
	}
	if m.s3Error != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("⚠ " + m.s3Error))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render("enter create · esc cancel"))
	return b.String()
}
