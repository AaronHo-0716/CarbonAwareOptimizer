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
	case "enter":
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
		Render("↑/↓ select · enter confirm · q quit"))
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
