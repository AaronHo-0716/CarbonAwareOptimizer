package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"

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

func getGridIntensity(token, region string) float64 {
	// Mock mapping to ElectricityMaps typical intensities (gCO2e/kWh)
	intensityMap := map[string]float64{
		"us-east-1":      390.0,
		"us-west-2":      150.0,
		"eu-west-1":      50.0,
		"ap-southeast-1": 450.0,
		"ap-southeast-5": 500.0, // mock high intensity
		"ca-central-1":   25.0,
	}

	if val, ok := intensityMap[region]; ok {
		return val
	}
	return 250.0 // global average fallback
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

func main() {
	var emToken string
	var tfPlanFile string

	if envContent, err := os.ReadFile(".env"); err == nil {
		content := string(envContent)
		for _, line := range strings.Split(content, "\n") {
			if strings.HasPrefix(line, "ELECTRICITYMAPS_TOKEN=") {
				emToken = strings.TrimPrefix(line, "ELECTRICITYMAPS_TOKEN=")
				break
			}
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

		var totalDailyOps float64
		var totalDailyEmb float64
		var totalInstances int
		var impacts []ResourceImpact

		// Constants for CCF Physics Formula
		const PUE = 1.135           // Typical AWS PUE
		const LifespanDays = 1460.0 // 4 years

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
					iType = "t3.medium" // Fallback mock for ASG if exact type isn't readily in plan
				} else if res.Type == "aws_db_instance" {
					if val, ok := res.Values["instance_class"].(string); ok {
						iType = strings.TrimPrefix(val, "db.")
					}
				}

				if iType == "" {
					iType = "m5.large" // Default fallback
				}

				totalInstances += int(count)
				vcpus := float64(getVCPUs(iType, vcpuMap))

				// Determine architecture
				coeff := x86Coeff
				if strings.Contains(iType, "g.") || strings.Contains(iType, "g2.") || strings.Contains(iType, "g3.") || strings.Contains(iType, "g4.") {
					coeff = armCoeff // Graviton
				}

				// 1. Operational Emissions (CCF Formula)
				// E_daily = (P_min + (P_max - P_min) * 0.5) * 24 * PUE
				// Note: Coeffs are per vCPU in Watts. We multiply by vCPUs, then divide by 1000 for kWh
				avgWattsPerVcpu := coeff.MinWatts + (coeff.MaxWatts-coeff.MinWatts)*0.5
				dailyKwhPerInstance := (avgWattsPerVcpu * vcpus * 24.0 * PUE) / 1000.0

				dailyOpsPerInstance := (dailyKwhPerInstance * gridIntensity) / 1000.0 // kgCO2e
				dailyOps := dailyOpsPerInstance * count

				// 2. Embodied Emissions
				embodiedTotal := embodiedData[iType] // kgCO2e
				if embodiedTotal == 0 {
					embodiedTotal = 1200.0 // 1.2 metric tons fallback
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
		fmt.Printf("%-64s %-6d %-10.3f %-10.3f %-10.3f\n",
			"TOTALS", totalInstances, totalDailyOps, totalDailyEmb, totalDailyOps+totalDailyEmb)

		// AI / Optimization Heuristics
		if gridIntensity > 200 {
			fmt.Println("\n💡 Optimization Suggestion: High Grid Intensity Detected!")
			fmt.Printf("   Consider shifting this workload from %s to a region with lower carbon intensity, such as eu-west-1 or ca-central-1.\n", region)
		}

		fmt.Println("\n💡 Architecture Suggestion: Graviton Migration")
		fmt.Println("   Consider migrating x86 workloads (e.g. m5, t3) to ARM64 Graviton instances (e.g. m6g, t4g) for up to 60% better performance-per-watt.")

	} else {
		fmt.Println("No Terraform plan provided.")
	}
}
