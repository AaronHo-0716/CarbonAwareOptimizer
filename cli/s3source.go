package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// s3ProbeCmd builds an S3 client and looks for a Carbon-Optimizer-tagged bucket.
// On success it emits s3ProbeMsg; on partial success (no bucket yet) the bucket
// field is empty so the caller can fall through to the local file picker.
func s3ProbeCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		client, err := newS3Client(ctx)
		if err != nil {
			return s3ProbeMsg{err: err}
		}
		bucket, err := client.detectBucket(ctx)
		if err != nil {
			return s3ProbeMsg{client: client, err: err}
		}
		return s3ProbeMsg{client: client, bucket: bucket}
	}
}

// s3ListCmd reads the run log from the bucket so the source screen can show
// prior plans.
func s3ListCmd(client *s3Client, bucket string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		defer cancel()
		plans, err := client.listPlans(ctx, bucket)
		return s3ListMsg{plans: plans, err: err}
	}
}

// s3DeleteCmd removes one prior run from the bucket.
func s3DeleteCmd(client *s3Client, bucket, planID string) tea.Cmd {
	return func() tea.Msg {
		if client == nil || bucket == "" {
			return s3DeleteMsg{planID: planID, err: fmt.Errorf("S3 client/bucket not initialised")}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := client.deletePlan(ctx, bucket, planID)
		return s3DeleteMsg{planID: planID, err: err}
	}
}

// updateS3Source handles arrow-key navigation + enter on the prior-runs list.
// Row 0 is always the "browse local plan" escape hatch.
func (m model) updateS3Source(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	total := len(m.s3Plans) + 1 // +1 for the "browse local" row
	switch msg.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "up", "k":
		if m.s3Cursor > 0 {
			m.s3Cursor--
		}
		return m, nil
	case "down", "j":
		if m.s3Cursor < total-1 {
			m.s3Cursor++
		}
		return m, nil
	case "d":
		if !m.s3Busy && m.s3Cursor > 0 && m.s3Cursor-1 < len(m.s3Plans) {
			m.s3DeleteTarget = m.s3Plans[m.s3Cursor-1].PlanID
			m.s3Error = ""
			m.state = stateS3DeleteConfirm
		}
		return m, nil
	case "enter":
		if m.s3Busy {
			return m, nil
		}
		if m.s3Cursor == 0 {
			m.state = stateFilePicker
			return m, nil
		}
		plan := m.s3Plans[m.s3Cursor-1]
		m.state = stateLoading
		m.loadMsg = fmt.Sprintf("Loading %s from S3…", plan.PlanID)
		return m, tea.Batch(m.spinner.Tick, s3LoadCmd(m.s3, m.s3Bucket, plan.PlanID))
	}
	return m, nil
}

// updateS3DeleteConfirm handles the y/n prompt that appears when the user
// presses 'd' on a prior run.
func (m model) updateS3DeleteConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "enter":
		planID := m.s3DeleteTarget
		m.s3DeleteTarget = ""
		m.s3Busy = true
		m.s3Error = ""
		m.state = stateS3Source
		if planID == "" {
			m.s3Busy = false
			return m, nil
		}
		return m, s3DeleteCmd(m.s3, m.s3Bucket, planID)
	case "n", "esc":
		m.s3DeleteTarget = ""
		m.state = stateS3Source
		return m, nil
	}
	return m, nil
}

// viewS3Source renders the prior-plans list with a header and footer hint.
func (m model) viewS3Source() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BF72")).Render("Carbon-Optimizer · S3 Source")
	subtitle := lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render(fmt.Sprintf("Bucket: %s", m.s3Bucket))
	b.WriteString(title)
	b.WriteString("\n")
	b.WriteString(subtitle)
	b.WriteString("\n\n")

	rowStyle := lipgloss.NewStyle()
	selStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BF72"))

	render := func(idx int, label string) {
		marker := "  "
		s := rowStyle
		if idx == m.s3Cursor {
			marker = "▶ "
			s = selStyle
		}
		b.WriteString(s.Render(marker + label))
		b.WriteString("\n")
	}

	render(0, "📂 Browse a local Terraform plan…")
	b.WriteString("\n")
	if len(m.s3Plans) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
			Render("  (no prior runs found — run an analysis and press Ctrl+S to save one)"))
		b.WriteString("\n")
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).Render("  Previous runs:"))
		b.WriteString("\n")
		for i, p := range m.s3Plans {
			ts := p.Timestamp.Local().Format("2006-01-02 15:04")
			label := fmt.Sprintf("📥 %-32s %s · %s", truncateRune(p.PlanID, 32), ts, p.Region)
			render(i+1, label)
		}
	}

	if m.s3Error != "" {
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("⚠ " + m.s3Error))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render("↑/↓ select · enter open · d delete · q quit"))
	return b.String()
}

// viewS3DeleteConfirm renders the deletion-confirmation overlay.
func (m model) viewS3DeleteConfirm() string {
	var b strings.Builder
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#00BF72")).
		Render("Delete run from S3")
	b.WriteString(title + "\n\n")
	b.WriteString("Permanently delete this run? All reports and the plan\n")
	b.WriteString("JSON will be removed from the bucket. This cannot be undone.\n\n")
	b.WriteString("  Plan-id: ")
	b.WriteString(lipgloss.NewStyle().Bold(true).Render(m.s3DeleteTarget))
	b.WriteString("\n")
	if m.s3Bucket != "" {
		b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
			Render(fmt.Sprintf("  Bucket:  %s", m.s3Bucket)))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Foreground(lipgloss.Color("240")).
		Render("y/enter confirm · n/esc cancel"))
	return b.String()
}

func truncateRune(s string, max int) string {
	if len(s) <= max {
		return s
	}
	if max <= 1 {
		return s[:max]
	}
	return s[:max-1] + "…"
}
