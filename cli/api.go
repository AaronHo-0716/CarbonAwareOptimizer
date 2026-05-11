package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// ── ElectricityMaps ───────────────────────────────────────────────────────────

func getGridIntensityQuiet(token, region string) float64 {
	client := &http.Client{Timeout: 6 * time.Second}
	url := fmt.Sprintf(
		"https://api.electricitymap.org/v3/carbon-intensity/latest?dataCenterProvider=aws&dataCenterRegion=%s",
		region,
	)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 250.0
	}
	req.Header.Set("auth-token", token)

	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		if resp != nil {
			resp.Body.Close()
		}
		return 250.0
	}
	defer resp.Body.Close()

	var r CarbonIntensityResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil || r.CarbonIntensity <= 0 {
		return 250.0
	}
	return r.CarbonIntensity
}

// ── Regional matrix ───────────────────────────────────────────────────────────

// buildRegionalMatrix fetches intensities for the peer regions of currentRegion
// concurrently and returns the top-5 (always including current) as MatrixRows
// plus a plain-text context string for the AI prompt.
func buildRegionalMatrix(
	token, currentRegion string,
	currentIntensity, currentOps float64,
	plan TFPlan,
	embodiedData map[string]float64,
	vcpuMap map[string]int,
	x86Coeff, armCoeff UseCoeff,
	networkProfile string,
	lambdaInvocations int,
	schedule UtilizationSchedule,
) ([]MatrixRow, string) {
	group := getRegionGroup(currentRegion)

	var mu sync.Mutex
	var wg sync.WaitGroup
	var results []RegionIntensity

	for _, r := range group {
		if r == currentRegion {
			continue
		}
		wg.Add(1)
		go func(reg string) {
			defer wg.Done()
			intensity := getGridIntensityQuiet(token, reg)
			if intensity != 250.0 { // skip fallback-only values
				mu.Lock()
				results = append(results, RegionIntensity{Region: reg, Intensity: intensity})
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()

	results = append(results, RegionIntensity{Region: currentRegion, Intensity: currentIntensity})
	sort.Slice(results, func(i, j int) bool { return results[i].Intensity < results[j].Intensity })

	// Keep top-5
	limit := 5
	if len(results) < limit {
		limit = len(results)
	}
	top := results[:limit]

	// Always include current region
	currentInTop := false
	for _, r := range top {
		if r.Region == currentRegion {
			currentInTop = true
			break
		}
	}
	if !currentInTop {
		top = append(top, RegionIntensity{Region: currentRegion, Intensity: currentIntensity})
		sort.Slice(top, func(i, j int) bool { return top[i].Intensity < top[j].Intensity })
	}

	var rows []MatrixRow
	var ctx strings.Builder
	ctx.WriteString("Live Regional Trade-off Matrix:\n")

	for _, res := range top {
		_, simOps, simEmb := calculateImpact(
			&plan, res.Region, res.Intensity,
			embodiedData, vcpuMap, x86Coeff, armCoeff,
			networkProfile, lambdaInvocations, schedule,
		)
		simTotal := simOps + simEmb
		costSummary, _ := buildCostSummary(&plan, res.Region, lambdaInvocations)

		regionName := res.Region
		if loc, ok := awsRegionNames[res.Region]; ok {
			regionName = fmt.Sprintf("%s (%s)", res.Region, loc)
		}
		isCurrent := res.Region == currentRegion
		if isCurrent {
			regionName += " ★"
		}

		deltaStr := "—"
		if !isCurrent && currentOps > 0 {
			pct := ((simOps - currentOps) / currentOps) * 100
			if pct >= 0 {
				deltaStr = fmt.Sprintf("+%.1f%%", pct)
			} else {
				deltaStr = fmt.Sprintf("%.1f%%", pct)
			}
		}

		rows = append(rows, MatrixRow{
			RegionName:  regionName,
			Intensity:   res.Intensity,
			Total:       simTotal,
			DeltaStr:    deltaStr,
			HourlyCost:  costSummary.TotalHourly,
			MonthlyCost: costSummary.TotalMonthly,
			CostKnown:   len(costSummary.Resources) > 0 && costSummary.UnavailableCount < len(costSummary.Resources),
		})
		if len(costSummary.Resources) > 0 {
			ctx.WriteString(fmt.Sprintf("- %s: %.2f gCO₂e/kWh, %.4f kg/day (Ops Δ: %s), $%.4f/hr, $%.2f/mo\n",
				regionName, res.Intensity, simTotal, deltaStr, costSummary.TotalHourly, costSummary.TotalMonthly))
		} else {
			ctx.WriteString(fmt.Sprintf("- %s: %.2f gCO₂e/kWh, %.4f kg/day (Ops Δ: %s)\n",
				regionName, res.Intensity, simTotal, deltaStr))
		}
	}

	return rows, ctx.String()
}

// ── AI suggestions ────────────────────────────────────────────────────────────

func getAISuggestions(
	apiKey string,
	top ResourceImpact,
	region string,
	gridIntensity float64,
	matrixContext string,
) string {
	if apiKey == "" || apiKey == "skip" {
		return fmt.Sprintf(
			"**Mock AI Response for `%s`**\n\n"+
				"1. **Graviton Equivalent:** Consider migrating to the `g` variant "+
				"(e.g. `m5` → `m6g`). Note: compiled languages (Go, Rust, C++) "+
				"migrate easily; JVM apps need arm64 builds; check all native deps.\n\n"+
				"2. **Greener Region:** Based on the matrix above, `eu-west-1` (Ireland) "+
				"or `ca-central-1` (Canada) typically offer lower grid intensity.\n\n"+
				"3. **Scheduling / Rightsizing:** Enable AWS Instance Scheduler to "+
				"power off non-production workloads outside business hours (~65%% savings).",
			top.Instance,
		)
	}

	regionPrompt := fmt.Sprintf(
		"2. A greener region — use this live trade-off data to pick the best alternative:\n%s",
		matrixContext,
	)
	prompt := fmt.Sprintf(
		"You are a GreenOps Specialist. This %s in %s (Grid: %.2f gCO₂e/kWh) "+
			"produces %.4f kg CO₂/day (Operational: %.4f kg, Embodied: %.4f kg, "+
			"4-year hardware lifespan). Provide concise recommendations:\n"+
			"1. A Graviton equivalent — include migration caveats "+
			"(compiled vs interpreted, arm64 library support, RDS Graviton support).\n"+
			"%s\n"+
			"3. Scheduling or rightsizing logic with estimated savings.",
		top.Instance, region, gridIntensity,
		top.TotalDaily, top.DailyOps, top.DailyEmb,
		regionPrompt,
	)

	reqBody := AIRequest{Model: "openai/gpt-4o-mini"}
	reqBody.Messages = append(reqBody.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: prompt})

	body, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest(
		"POST",
		"https://openrouter.ai/api/v1/chat/completions",
		strings.NewReader(string(body)),
	)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/carbon-optimizer")
	req.Header.Set("X-Title", "Carbon Optimizer")

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != http.StatusOK {
		return "⚠  Failed to fetch AI suggestions — check your OpenRouter API key."
	}
	defer resp.Body.Close()

	var aiResp AIResponse
	if err := json.NewDecoder(resp.Body).Decode(&aiResp); err != nil || len(aiResp.Choices) == 0 {
		return "⚠  Could not parse the AI response."
	}
	return aiResp.Choices[0].Message.Content
}
