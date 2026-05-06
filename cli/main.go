package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/huh"
)

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
	} `json:"configuration"`
}

type UseCoeff struct {
	MinWatts float64
	MaxWatts float64
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

func getGridIntensity(token, region string) float64 {
	client := &http.Client{}

	url := fmt.Sprintf("https://api.electricitymap.org/v3/carbon-intensity/latest?dataCenterProvider=aws&dataCenterRegion=%s", region)
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
		fmt.Printf("⚠️ Could not find or fetch zone mapping for region %s, using global average intensity.\n", region)
		return 250.0
	}
	defer resp.Body.Close()

	var intensityResp CarbonIntensityResponse
	if err := json.NewDecoder(resp.Body).Decode(&intensityResp); err != nil {
		return 250.0
	}

	if intensityResp.CarbonIntensity > 0 {
		return intensityResp.CarbonIntensity
	}

	return 250.0
}

func getGridIntensityQuiet(token, region string) float64 {
	client := &http.Client{Timeout: 5 * time.Second}

	url := fmt.Sprintf("https://api.electricitymap.org/v3/carbon-intensity/latest?dataCenterProvider=aws&dataCenterRegion=%s", region)
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

	var intensityResp CarbonIntensityResponse
	if err := json.NewDecoder(resp.Body).Decode(&intensityResp); err != nil {
		return 250.0
	}

	if intensityResp.CarbonIntensity > 0 {
		return intensityResp.CarbonIntensity
	}

	return 250.0
}

type RegionIntensity struct {
	Region    string
	Intensity float64
}

func getTopAsianRegions(token string) string {
	regions := []string{
		"ap-east-1", "ap-northeast-1", "ap-northeast-2", "ap-northeast-3",
		"ap-south-1", "ap-south-2", "ap-southeast-1", "ap-southeast-2",
		"ap-southeast-3", "ap-southeast-4", "ap-southeast-5",
	}

	var mu sync.Mutex
	var wg sync.WaitGroup
	var results []RegionIntensity

	for _, r := range regions {
		wg.Add(1)
		go func(reg string) {
			defer wg.Done()
			intensity := getGridIntensityQuiet(token, reg)
			if intensity > 0 && intensity != 250.0 { // ignore fallback values
				mu.Lock()
				results = append(results, RegionIntensity{Region: reg, Intensity: intensity})
				mu.Unlock()
			}
		}(r)
	}
	wg.Wait()

	sort.Slice(results, func(i, j int) bool {
		return results[i].Intensity < results[j].Intensity
	})

	limit := 5
	if len(results) < 5 {
		limit = len(results)
	}

	if limit == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("Here are the top greenest Asian regions you can choose from based on live data:\n")
	for i := 0; i < limit; i++ {
		sb.WriteString(fmt.Sprintf("- %s: %.2f gCO2e/kWh\n", results[i].Region, results[i].Intensity))
	}
	return sb.String()
}

func parseEmbodiedEmissions() map[string]float64 {
	embodied := make(map[string]float64)
	file, err := os.Open("coefficients-aws-embodied.csv")
	if err != nil {
		return embodied
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return embodied
	}

	for i, record := range records {
		if i == 0 || len(record) < 7 {
			continue
		}
		instanceType := record[1]
		total, _ := strconv.ParseFloat(record[6], 64)
		embodied[instanceType] = total
	}
	return embodied
}

func parseInstanceVCPUs() map[string]int {
	vcpus := make(map[string]int)
	file, err := os.Open("aws-instances-latest-2026.csv")
	if err != nil {
		return vcpus
	}
	defer file.Close()

	reader := csv.NewReader(file)
	records, err := reader.ReadAll()
	if err != nil {
		return vcpus
	}

	for i, record := range records {
		if i == 0 || len(record) < 3 {
			continue
		}
		instanceType := record[1]
		v, _ := strconv.Atoi(record[2])
		vcpus[instanceType] = v
	}
	return vcpus
}

func getUseCoefficients() (UseCoeff, UseCoeff) {
	// Fallback coefficients if file is missing
	x86 := UseCoeff{MinWatts: 0.8, MaxWatts: 4.0}
	arm := UseCoeff{MinWatts: 0.47, MaxWatts: 1.7}

	file, err := os.Open("coefficients-aws-use.csv")
	if err == nil {
		defer file.Close()
		reader := csv.NewReader(file)
		records, err := reader.ReadAll()
		if err == nil {
			var x86MinSum, x86MaxSum, armMinSum, armMaxSum float64
			var x86Count, armCount float64
			for i, rec := range records {
				if i == 0 || len(rec) < 4 {
					continue
				}
				minW, _ := strconv.ParseFloat(rec[2], 64)
				maxW, _ := strconv.ParseFloat(rec[3], 64)
				arch := strings.ToLower(rec[1])

				if strings.Contains(arch, "graviton") {
					armMinSum += minW
					armMaxSum += maxW
					armCount++
				} else {
					x86MinSum += minW
					x86MaxSum += maxW
					x86Count++
				}
			}
			if x86Count > 0 {
				x86.MinWatts = x86MinSum / x86Count
				x86.MaxWatts = x86MaxSum / x86Count
			}
			if armCount > 0 {
				arm.MinWatts = armMinSum / armCount
				arm.MaxWatts = armMaxSum / armCount
			}
		}
	}
	return x86, arm
}

func getVCPUs(instanceType string, vcpuMap map[string]int) int {
	if val, ok := vcpuMap[instanceType]; ok {
		return val
	}
	// Rough heuristic fallback
	if strings.Contains(instanceType, "nano") || strings.Contains(instanceType, "micro") || strings.Contains(instanceType, "small") || strings.Contains(instanceType, "medium") || strings.Contains(instanceType, "large") {
		return 2
	} else if strings.Contains(instanceType, "xlarge") {
		return 4
	} else if strings.Contains(instanceType, "2xlarge") {
		return 8
	}
	return 2
}

func calculateImpact(plan *TFPlan, region string, gridIntensity float64, embodiedData map[string]float64, vcpuMap map[string]int, x86Coeff, armCoeff UseCoeff) ([]ResourceImpact, float64, float64) {
	var totalDailyOps float64
	var totalDailyEmb float64
	var impacts []ResourceImpact

	const PUE = 1.135
	const LifespanDays = 1460.0

	for _, res := range plan.PlannedValues.RootModule.Resources {
		if res.Mode != "managed" {
			continue
		}

		if res.Type == "aws_instance" || res.Type == "aws_autoscaling_group" || res.Type == "aws_db_instance" {
			var iType string
			count := 1.0

			if res.Type == "aws_instance" {
				if val, ok := res.Values["instance_type"].(string); ok {
					iType = val
				}
			} else if res.Type == "aws_autoscaling_group" {
				if val, ok := res.Values["desired_capacity"].(float64); ok {
					count = val
				}
				iType = "t3.medium"
			} else if res.Type == "aws_db_instance" {
				if val, ok := res.Values["instance_class"].(string); ok {
					iType = strings.TrimPrefix(val, "db.")
				}
			}

			if iType == "" {
				iType = "m5.large"
			}

			vcpus := float64(getVCPUs(iType, vcpuMap))

			coeff := x86Coeff
			if strings.Contains(iType, "g.") || strings.Contains(iType, "g2.") || strings.Contains(iType, "g3.") || strings.Contains(iType, "g4.") || strings.Contains(iType, "r7g") {
				coeff = armCoeff
			}

			avgWattsPerVcpu := coeff.MinWatts + (coeff.MaxWatts-coeff.MinWatts)*0.5
			dailyKwhPerInstance := (avgWattsPerVcpu * vcpus * 24.0 * PUE) / 1000.0

			dailyOpsPerInstance := (dailyKwhPerInstance * gridIntensity) / 1000.0
			dailyOps := dailyOpsPerInstance * count

			embodiedTotal := embodiedData[iType]
			if embodiedTotal == 0 {
				embodiedTotal = 1200.0
			}
			dailyEmbPerInstance := embodiedTotal / LifespanDays
			dailyEmb := dailyEmbPerInstance * count

			impacts = append(impacts, ResourceImpact{
				Type:       res.Type,
				Name:       res.Name,
				Instance:   iType,
				Count:      int(count),
				DailyOps:   dailyOps,
				DailyEmb:   dailyEmb,
				TotalDaily: dailyOps + dailyEmb,
			})

			totalDailyOps += dailyOps
			totalDailyEmb += dailyEmb
		}
	}
	return impacts, totalDailyOps, totalDailyEmb
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

func getAISuggestions(apiKey string, topResource ResourceImpact, region string, gridIntensity float64, asianContext string) string {
	var regionPrompt string
	if asianContext != "" {
		regionPrompt = fmt.Sprintf("2. A greener region with lower grid intensity. The user is currently in Asia. Use this live data to suggest the best alternative:\n%s", asianContext)
	} else {
		regionPrompt = "2. A greener region with lower grid intensity (e.g., if in Asia, suggest a green Asian region like ap-northeast-3 or similar, or global alternatives)."
	}

	prompt := fmt.Sprintf("You are a GreenOps Specialist. This %s in %s (Grid Intensity: %.2f gCO2e/kWh) produces %.3f kg of CO2 daily. Suggest:\n1. A Graviton equivalent. Explicitly mention the caveats of migrating to Graviton (e.g., which applications can or cannot migrate easily, compiled vs interpreted languages, dependencies).\n%s\n3. Scheduling or Rightsizing logic.", topResource.Instance, region, gridIntensity, topResource.TotalDaily, regionPrompt)

	fmt.Println("\n🤖 Fetching AI Insights...")

	if apiKey == "" || apiKey == "skip" {
		return fmt.Sprintf("Mock AI Response for %s:\n1. Graviton Equivalent: Consider migrating to the 'g' variant (e.g. if m5, use m6g).\n2. Greener Region: Move to eu-west-1 (Ireland) or ca-central-1 (Canada).\n3. Scheduling: Turn off outside of business hours (saves ~65%%).", topResource.Instance)
	}

	reqBody := AIRequest{
		Model: "openai/gpt-4o-mini",
	}
	reqBody.Messages = append(reqBody.Messages, struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}{Role: "user", Content: prompt})

	jsonBody, _ := json.Marshal(reqBody)
	req, _ := http.NewRequest("POST", "https://openrouter.ai/api/v1/chat/completions", strings.NewReader(string(jsonBody)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("HTTP-Referer", "https://github.com/carbon-optimizer")
	req.Header.Set("X-Title", "Carbon Optimizer")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil || resp.StatusCode != 200 {
		return "⚠️ Failed to fetch AI suggestions. Please check your API Key."
	}
	defer resp.Body.Close()

	var aiResp AIResponse
	if err := json.NewDecoder(resp.Body).Decode(&aiResp); err != nil || len(aiResp.Choices) == 0 {
		return "⚠️ Could not parse AI response."
	}

	return aiResp.Choices[0].Message.Content
}

func main() {
	var emToken string
	var openRouterToken string
	var tfPlanFile string

	if envContent, err := os.ReadFile(".env"); err == nil {
		content := string(envContent)
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "ELECTRICITYMAPS_TOKEN=") {
				emToken = strings.TrimPrefix(line, "ELECTRICITYMAPS_TOKEN=")
			}
			if strings.HasPrefix(line, "OPENROUTER_API_KEY=") {
				openRouterToken = strings.TrimPrefix(line, "OPENROUTER_API_KEY=")
			}
		}
	}

	if openRouterToken == "" {
		huh.NewForm(
			huh.NewGroup(
				huh.NewInput().Title("OpenRouter API Key (optional, press enter to skip)").Value(&openRouterToken),
			),
		).Run()
		if openRouterToken != "" && openRouterToken != "skip" {
			// Append to .env safely
			f, err := os.OpenFile(".env", os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
			if err == nil {
				f.WriteString(fmt.Sprintf("OPENROUTER_API_KEY=%s\n", openRouterToken))
				f.Close()
			}
		} else {
			openRouterToken = "skip"
		}
	}

	if emToken == "" {
		huh.NewForm(
			huh.NewGroup(
				huh.NewInput().Title("ElectricityMaps API Token").Value(&emToken),
			),
		).Run()
		if emToken != "" {
			envContent := fmt.Sprintf("CARBON_PROVIDER=electricitymaps\nELECTRICITYMAPS_TOKEN=%s\n", emToken)
			os.WriteFile(".env", []byte(envContent), 0600)
		}
	}

	if emToken == "" {
		fmt.Println("ElectricityMaps token is required")
		os.Exit(1)
	}

	if len(os.Args) > 1 {
		tfPlanFile = os.Args[1]
	} else {
		huh.NewForm(
			huh.NewGroup(
				huh.NewInput().Title("Terraform Plan JSON file path").Value(&tfPlanFile),
			),
		).Run()
	}

	fmt.Println("🌍 Carbon Optimizer - Static Infrastructure Carbon Estimator")
	fmt.Println("🔑 API Key configured for ElectricityMaps.")

	if tfPlanFile != "" {
		fmt.Printf("📄 Analyzing Terraform Plan: %s\n", tfPlanFile)

		planData, err := os.ReadFile(tfPlanFile)
		if err != nil {
			fmt.Printf("❌ Failed to read plan file: %v\n", err)
			os.Exit(1)
		}

		var plan TFPlan
		if err := json.Unmarshal(planData, &plan); err != nil {
			fmt.Printf("❌ Failed to parse plan JSON: %v\n", err)
			os.Exit(1)
		}

		// Load datasets
		embodiedData := parseEmbodiedEmissions()
		vcpuMap := parseInstanceVCPUs()
		x86Coeff, armCoeff := getUseCoefficients()

		region := "us-east-1" // default

		// 1. Try variable "aws_region"
		if v, ok := plan.Variables["aws_region"]; ok {
			if s, ok := v.Value.(string); ok && s != "" {
				region = s
			}
		}

		// 2. Try to extract region from configuration
		if region == "us-east-1" {
			for k, v := range plan.Configuration.ProviderConfig {
				if k == "aws" || k == "aws.default" {
					if expr, ok := v.Expressions["region"]; ok {
						if expr.ConstantValue != "" {
							region = expr.ConstantValue
						}
					}
				}
			}
		}

		gridIntensity := getGridIntensity(emToken, region)
		fmt.Printf("📍 Region identified: %s (Intensity: %.2f gCO2e/kWh)\n\n", region, gridIntensity)

		impacts, totalDailyOps, totalDailyEmb := calculateImpact(&plan, region, gridIntensity, embodiedData, vcpuMap, x86Coeff, armCoeff)

		totalInstances := 0
		var topResource ResourceImpact
		highestDaily := -1.0

		for _, imp := range impacts {
			totalInstances += imp.Count
			if imp.TotalDaily > highestDaily {
				highestDaily = imp.TotalDaily
				topResource = imp
			}
		}

		fmt.Println("📊 --- Granular Resource Carbon Impact Assessment ---")
		fmt.Printf("%-25s %-25s %-12s %-6s %-10s %-10s %-10s\n", "Type", "Name", "Instance", "Qty", "Ops(kg)", "Emb(kg)", "Total(kg)")
		fmt.Println(strings.Repeat("-", 103))

		for _, imp := range impacts {
			typeShort := imp.Type
			if len(typeShort) > 23 {
				typeShort = typeShort[:20] + "..."
			}
			nameShort := imp.Name
			if len(nameShort) > 23 {
				nameShort = nameShort[:20] + "..."
			}

			fmt.Printf("%-25s %-25s %-12s %-6d %-10.3f %-10.3f %-10.3f\n",
				typeShort, nameShort, imp.Instance, imp.Count, imp.DailyOps, imp.DailyEmb, imp.TotalDaily)
		}

		fmt.Println(strings.Repeat("-", 103))
		originalTotal := totalDailyOps + totalDailyEmb
		fmt.Printf("%-64s %-6d %-10.3f %-10.3f %-10.3f\n",
			"TOTALS", totalInstances, totalDailyOps, totalDailyEmb, originalTotal)

		if highestDaily > 0 {
			fmt.Printf("\n🔥 Top Emitter Detected: %s (%s) emitting %.3f kg CO2/day.\n", topResource.Name, topResource.Instance, highestDaily)

			var asianContext string
			if strings.HasPrefix(region, "ap-") {
				fmt.Println("🌏 Asian region detected, discovering the greenest data centers...")
				asianContext = getTopAsianRegions(emToken)
			}

			aiSuggestion := getAISuggestions(openRouterToken, topResource, region, gridIntensity, asianContext)

			renderedSuggestion, err := glamour.Render(aiSuggestion, "dark")
			if err != nil {
				renderedSuggestion = aiSuggestion
			}

			fmt.Printf("\n🌿 AI GreenOps Recommendation:\n%s\n", renderedSuggestion)
		}

		// Interactive Optimization Loop
		for {
			var optChoice string
			err := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title("Apply an Optimization to view Projected Carbon Savings:").
						Options(
							huh.NewOption("1. Migrate all compatible instances to Graviton", "graviton"),
							huh.NewOption("2. Move to a Greener Region", "region"),
							huh.NewOption("3. Exit", "exit"),
						).
						Value(&optChoice),
				),
			).Run()

			if err != nil || optChoice == "exit" {
				fmt.Println("Exiting optimization loop.")
				break
			}

			newRegion := region
			var simPlan TFPlan = plan // Soft copy for simulation

			if optChoice == "graviton" {
				fmt.Println("\n🔄 Simulating Graviton Migration...")
				for i, res := range simPlan.PlannedValues.RootModule.Resources {
					if res.Mode == "managed" {
						if res.Type == "aws_instance" {
							if val, ok := res.Values["instance_type"].(string); ok {
								if strings.Contains(val, "m5.") {
									simPlan.PlannedValues.RootModule.Resources[i].Values["instance_type"] = strings.Replace(val, "m5.", "m6g.", 1)
								} else if strings.Contains(val, "t3.") {
									simPlan.PlannedValues.RootModule.Resources[i].Values["instance_type"] = strings.Replace(val, "t3.", "t4g.", 1)
								}
							}
						} else if res.Type == "aws_db_instance" {
							if val, ok := res.Values["instance_class"].(string); ok {
								if strings.Contains(val, "m5.") {
									simPlan.PlannedValues.RootModule.Resources[i].Values["instance_class"] = strings.Replace(val, "m5.", "m6g.", 1)
								} else if strings.Contains(val, "t3.") {
									simPlan.PlannedValues.RootModule.Resources[i].Values["instance_class"] = strings.Replace(val, "t3.", "t4g.", 1)
								}
							}
						}
					}
				}
			} else if optChoice == "region" {
				huh.NewForm(
					huh.NewGroup(
						huh.NewInput().Title("Enter new AWS Region (e.g. eu-west-1, ca-central-1, ap-northeast-3)").Value(&newRegion),
					),
				).Run()
				fmt.Printf("\n🔄 Simulating Move to %s...\n", newRegion)
			}

			newGridIntensity := getGridIntensity(emToken, newRegion)
			_, simOps, simEmb := calculateImpact(&simPlan, newRegion, newGridIntensity, embodiedData, vcpuMap, x86Coeff, armCoeff)
			simTotal := simOps + simEmb

			fmt.Printf("\n📈 Projected Savings after Optimization:\n")
			fmt.Printf("   Original Total CO2/day: %.3f kg\n", originalTotal)
			fmt.Printf("   Projected Total CO2/day: %.3f kg\n", simTotal)
			savings := originalTotal - simTotal
			percent := (savings / originalTotal) * 100
			if savings > 0 {
				fmt.Printf("   ✨ You saved %.3f kg CO2/day (%.1f%% reduction)!\n\n", savings, percent)
			} else {
				fmt.Printf("   ⚠️ This optimization increased or did not change emissions.\n\n")
			}
		}

	} else {
		fmt.Println("No Terraform plan provided.")
	}
}
