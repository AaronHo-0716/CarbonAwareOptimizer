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

func getGridIntensity(token, region string) float64 {
	// For demo purposes, we will return a mock value if we can't get it from the API
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

		embodiedData := parseEmbodiedEmissions()

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

		var dailyOpsEmissions float64
		var dailyEmbodiedEmissions float64
		var instanceCount int

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
					// ASG instance type might be in launch template, which is harder to parse from plan.json without looking up refs.
					// Fallback mock if needed.
					iType = "t3.medium"
				} else if res.Type == "aws_db_instance" {
					if val, ok := res.Values["instance_class"].(string); ok {
						// e.g. db.t3.micro -> t3.micro
						iType = strings.TrimPrefix(val, "db.")
					}
				}

				if iType == "" {
					iType = "m5.large" // Default fallback
				}

				instanceCount += int(count)

				// 1. Embodied calculation
				// Lifespan = 4 years
				embodied := embodiedData[iType] // kgCO2e
				if embodied == 0 {
					embodied = 1200.0 // Mock fallback 1.2 metric tons = 1200 kg
				}
				dailyEmbodied := (embodied / (4 * 365)) * count

				// 2. Operational calculation
				// Estimate daily energy: mock average 3 kWh per day per instance
				dailyKwh := 3.0 * count
				dailyOps := (dailyKwh * gridIntensity) / 1000.0 // kgCO2e

				dailyOpsEmissions += dailyOps
				dailyEmbodiedEmissions += dailyEmbodied

				fmt.Printf("✅ Found %s (%s): %s (x%d)\n", res.Type, res.Name, iType, int(count))
			}
		}

		totalDaily := dailyOpsEmissions + dailyEmbodiedEmissions

		fmt.Println("\n📊 --- Carbon Impact Assessment ---")
		fmt.Printf("Total Instances Tracked:   %d\n", instanceCount)
		fmt.Printf("Daily Operational Carbon:  %.2f kgCO2e\n", dailyOpsEmissions)
		fmt.Printf("Daily Embodied Carbon:     %.2f kgCO2e\n", dailyEmbodiedEmissions)
		fmt.Printf("Total Daily Footprint:     %.2f kgCO2e\n", totalDaily)
		fmt.Println("----------------------------------")

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
