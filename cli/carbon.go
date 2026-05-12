package main

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ── Region lookup tables ──────────────────────────────────────────────────────

var regionGroups = map[string][]string{
	"americas":           {"us-east-1", "us-east-2", "us-west-1", "us-west-2", "ca-central-1", "ca-west-1", "mx-central-1", "sa-east-1"},
	"europe":             {"eu-central-1", "eu-west-1", "eu-west-2", "eu-south-1", "eu-west-3", "eu-south-2", "eu-north-1", "eu-central-2"},
	"asia-pacific":       {"ap-east-1", "ap-south-2", "ap-southeast-3", "ap-southeast-5", "ap-southeast-4", "ap-south-1", "ap-northeast-3", "ap-northeast-2", "ap-southeast-2", "ap-southeast-7", "ap-northeast-1", "ap-southeast-1", "ap-southeast-6"},
	"middle-east-africa": {"af-south-1", "il-central-1", "me-central-1", "me-south-1"},
}

var awsRegionNames = map[string]string{
	"af-south-1":     "Cape Town",
	"ap-east-1":      "Hong Kong",
	"ap-northeast-1": "Tokyo",
	"ap-northeast-2": "Seoul",
	"ap-northeast-3": "Osaka",
	"ap-south-1":     "Mumbai",
	"ap-south-2":     "Hyderabad",
	"ap-southeast-1": "Singapore",
	"ap-southeast-2": "Sydney",
	"ap-southeast-3": "Jakarta",
	"ap-southeast-4": "Melbourne",
	"ap-southeast-5": "Malaysia",
	"ap-southeast-6": "New Zealand",
	"ap-southeast-7": "Thailand",
	"ca-central-1":   "Canada Central",
	"ca-west-1":      "Calgary",
	"eu-central-1":   "Frankfurt",
	"eu-central-2":   "Zurich",
	"eu-north-1":     "Stockholm",
	"eu-south-1":     "Milan",
	"eu-south-2":     "Spain",
	"eu-west-1":      "Ireland",
	"eu-west-2":      "London",
	"eu-west-3":      "Paris",
	"il-central-1":   "Tel Aviv",
	"me-central-1":   "UAE",
	"me-south-1":     "Bahrain",
	"mx-central-1":   "Mexico",
	"sa-east-1":      "São Paulo",
	"us-east-1":      "N. Virginia",
	"us-east-2":      "Ohio",
	"us-west-1":      "N. California",
	"us-west-2":      "Oregon",
}

func getRegionGroup(region string) []string {
	switch {
	case strings.HasPrefix(region, "us-"), strings.HasPrefix(region, "ca-"),
		strings.HasPrefix(region, "sa-"), strings.HasPrefix(region, "mx-"):
		return regionGroups["americas"]
	case strings.HasPrefix(region, "eu-"), strings.HasPrefix(region, "uk-"):
		return regionGroups["europe"]
	case strings.HasPrefix(region, "ap-"):
		return regionGroups["asia-pacific"]
	case strings.HasPrefix(region, "me-"), strings.HasPrefix(region, "af-"),
		strings.HasPrefix(region, "il-"):
		return regionGroups["middle-east-africa"]
	}
	return []string{"us-east-1", "us-west-2", "eu-west-1", "ap-northeast-1", "ap-southeast-1"}
}

// ── Plan loading ──────────────────────────────────────────────────────────────

func loadPlan(path string) (TFPlan, []byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return TFPlan{}, nil, fmt.Errorf("cannot access path: %w", err)
	}

	var planData []byte

	if !info.IsDir() && strings.HasSuffix(path, ".json") {
		// Pre-computed JSON plan — read directly.
		planData, err = os.ReadFile(path)
		if err != nil {
			return TFPlan{}, nil, fmt.Errorf("reading plan file: %w", err)
		}
	} else {
		// Directory or .tf file — run terraform init / plan / show.
		dir := path
		if !info.IsDir() {
			dir = filepath.Dir(path) // .tf file: use its directory
		}
		if _, err := exec.LookPath("terraform"); err != nil {
			return TFPlan{}, nil, fmt.Errorf("terraform CLI not found — provide a plan.json instead")
		}

		// Build the environment: inherit current env, ensure AWS_PROFILE is set.
		tfEnv := os.Environ()
		if os.Getenv("AWS_PROFILE") == "" {
			tfEnv = append(tfEnv, "AWS_PROFILE=lifeng")
		}

		initCmd := exec.Command("terraform", "init")
		initCmd.Dir = dir
		initCmd.Env = tfEnv
		if out, err := initCmd.CombinedOutput(); err != nil {
			return TFPlan{}, nil, fmt.Errorf("terraform init failed:\n%s", string(out))
		}

		planFile := ".carbon_plan.tfplan"
		planCmd := exec.Command("terraform", "plan", "-out="+planFile)
		planCmd.Dir = dir
		planCmd.Env = tfEnv
		if out, err := planCmd.CombinedOutput(); err != nil {
			return TFPlan{}, nil, fmt.Errorf("terraform plan failed:\n%s", string(out))
		}
		defer os.Remove(filepath.Join(dir, planFile))

		showCmd := exec.Command("terraform", "show", "-json", planFile)
		showCmd.Dir = dir
		showCmd.Env = tfEnv
		planData, err = showCmd.Output()
		if err != nil {
			return TFPlan{}, nil, fmt.Errorf("terraform show failed: %w", err)
		}
	}

	var plan TFPlan
	if err := json.Unmarshal(planData, &plan); err != nil {
		return TFPlan{}, nil, fmt.Errorf("parsing plan JSON: %w", err)
	}
	return plan, planData, nil
}

func extractRegion(plan TFPlan) string {
	if v, ok := plan.Variables["aws_region"]; ok {
		if s, ok := v.Value.(string); ok && s != "" {
			return s
		}
	}
	for k, v := range plan.Configuration.ProviderConfig {
		if k == "aws" || k == "aws.default" {
			if expr, ok := v.Expressions["region"]; ok && expr.ConstantValue != "" {
				return expr.ConstantValue
			}
		}
	}
	return "us-east-1"
}

// detectResourceTypes scans the plan and reports whether network-related and
// Lambda resources are present (used to show/hide side-panel options).
func detectResourceTypes(plan TFPlan) (hasNetwork, hasLambda bool) {
	for _, res := range plan.PlannedValues.RootModule.Resources {
		if res.Mode != "managed" {
			continue
		}
		switch res.Type {
		case "aws_lb", "aws_alb", "aws_elb", "aws_instance", "aws_autoscaling_group":
			hasNetwork = true
		case "aws_lambda_function":
			hasLambda = true
		}
	}
	return
}

// ── CSV data loaders ──────────────────────────────────────────────────────────

func parseEmbodiedEmissions() map[string]float64 {
	out := make(map[string]float64)
	f, err := os.Open("coefficients-aws-embodied.csv")
	if err != nil {
		return out
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return out
	}
	for i, r := range recs {
		if i == 0 || len(r) < 7 {
			continue
		}
		v, _ := strconv.ParseFloat(r[6], 64)
		out[r[1]] = v
	}
	return out
}

func parseInstanceVCPUs() map[string]int {
	out := make(map[string]int)
	f, err := os.Open("aws-instances-latest-2026.csv")
	if err != nil {
		return out
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return out
	}
	for i, r := range recs {
		if i == 0 || len(r) < 3 {
			continue
		}
		v, _ := strconv.Atoi(r[2])
		out[r[1]] = v
	}
	return out
}

func getUseCoefficients() (x86, arm UseCoeff) {
	x86 = UseCoeff{MinWatts: 0.8, MaxWatts: 4.0}
	arm = UseCoeff{MinWatts: 0.47, MaxWatts: 1.7}

	f, err := os.Open("coefficients-aws-use.csv")
	if err != nil {
		return
	}
	defer f.Close()
	recs, err := csv.NewReader(f).ReadAll()
	if err != nil {
		return
	}

	var x86MinS, x86MaxS, armMinS, armMaxS, x86N, armN float64
	for i, r := range recs {
		if i == 0 || len(r) < 4 {
			continue
		}
		minW, _ := strconv.ParseFloat(r[2], 64)
		maxW, _ := strconv.ParseFloat(r[3], 64)
		if strings.Contains(strings.ToLower(r[1]), "graviton") {
			armMinS += minW
			armMaxS += maxW
			armN++
		} else {
			x86MinS += minW
			x86MaxS += maxW
			x86N++
		}
	}
	if x86N > 0 {
		x86 = UseCoeff{MinWatts: x86MinS / x86N, MaxWatts: x86MaxS / x86N}
	}
	if armN > 0 {
		arm = UseCoeff{MinWatts: armMinS / armN, MaxWatts: armMaxS / armN}
	}
	return
}

func getVCPUs(instanceType string, vcpuMap map[string]int) int {
	if v, ok := vcpuMap[instanceType]; ok {
		return v
	}
	switch {
	case strings.Contains(instanceType, "nano"), strings.Contains(instanceType, "micro"),
		strings.Contains(instanceType, "small"):
		return 1
	case strings.Contains(instanceType, "medium"), strings.Contains(instanceType, "large"):
		return 2
	case strings.Contains(instanceType, "2xlarge"):
		return 8
	case strings.Contains(instanceType, "xlarge"):
		return 4
	}
	return 2
}

// ── Carbon impact calculation ─────────────────────────────────────────────────

func calculateImpact(
	plan *TFPlan,
	region string, gridIntensity float64,
	embodiedData map[string]float64, vcpuMap map[string]int,
	x86Coeff, armCoeff UseCoeff,
	networkProfile string, lambdaInvocations int,
	schedule UtilizationSchedule,
) ([]ResourceImpact, float64, float64) {
	const PUE = 1.135
	const LifespanDays = 1460.0

	var totalOps, totalEmb float64
	var impacts []ResourceImpact

	for _, res := range plan.PlannedValues.RootModule.Resources {
		if res.Mode != "managed" {
			continue
		}

		switch res.Type {

		// ── Compute ──────────────────────────────────────────────────────────
		case "aws_instance", "aws_autoscaling_group", "aws_db_instance":
			var iType string
			count := 1.0
			var dbStorageGB float64

			switch res.Type {
			case "aws_instance":
				iType, _ = res.Values["instance_type"].(string)
			case "aws_autoscaling_group":
				if v, ok := res.Values["desired_capacity"].(float64); ok {
					count = v
				}
				iType = "t3.medium"
			case "aws_db_instance":
				if v, ok := res.Values["instance_class"].(string); ok {
					iType = strings.TrimPrefix(v, "db.")
				}
				if v, ok := res.Values["allocated_storage"].(float64); ok {
					dbStorageGB = v
				}
			}
			if iType == "" {
				iType = "m5.large"
			}

			vcpus := float64(getVCPUs(iType, vcpuMap))
			coeff := x86Coeff
			// Rough Graviton detection: instance families ending in 'g'
			if strings.Contains(iType, "r7g") || strings.HasSuffix(strings.Split(iType, ".")[0], "g") {
				coeff = armCoeff
			}

			utilization := scheduleUtilization(schedule)
			avgW := coeff.MinWatts + (coeff.MaxWatts-coeff.MinWatts)*utilization
			dailyKwh := (avgW * vcpus * 24.0 * PUE) / 1000.0
			dailyOps := (dailyKwh * gridIntensity / 1000.0) * count

			emb := embodiedData[iType]
			if emb == 0 {
				emb = 1200.0
			}
			dailyEmb := (emb / LifespanDays) * count

			// DB storage overhead
			if res.Type == "aws_db_instance" && dbStorageGB > 0 {
				sKwh := (0.0012 * dbStorageGB * 24.0 * PUE) / 1000.0
				dailyOps += (sKwh * gridIntensity / 1000.0) * count
				dailyEmb += ((dbStorageGB / 1024.0) * 50.0 / LifespanDays) * count
			}

			// Networking overhead for instances
			if res.Type == "aws_instance" || res.Type == "aws_autoscaling_group" {
				tGB := trafficByProfile(networkProfile)
				netKwh := (tGB / 30.0) * 0.001 * PUE
				dailyOps += (netKwh * gridIntensity / 1000.0) * count
			}

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: iType,
				Count: int(count), DailyOps: dailyOps, DailyEmb: dailyEmb, TotalDaily: dailyOps + dailyEmb,
			})
			totalOps += dailyOps
			totalEmb += dailyEmb

		// ── EBS Storage ───────────────────────────────────────────────────────
		case "aws_ebs_volume":
			var size float64
			if v, ok := res.Values["size"].(float64); ok {
				size = v
			}
			vType, _ := res.Values["type"].(string)
			isSSD := vType == "gp2" || vType == "gp3" || vType == "io1" || vType == "io2" || vType == ""
			wPerGB, embPerTB := 0.0065, 20.0
			if isSSD {
				wPerGB, embPerTB = 0.0012, 50.0
			}
			dailyKwh := (wPerGB * size * 24.0 * PUE) / 1000.0
			dailyOps := (dailyKwh * gridIntensity) / 1000.0
			dailyEmb := (size / 1024.0) * embPerTB / LifespanDays

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: "Storage",
				Count: 1, DailyOps: dailyOps, DailyEmb: dailyEmb, TotalDaily: dailyOps + dailyEmb,
			})
			totalOps += dailyOps
			totalEmb += dailyEmb

		// ── Load Balancers ────────────────────────────────────────────────────
		case "aws_lb", "aws_alb", "aws_elb":
			tGB := trafficByProfile(networkProfile)
			netKwh := (tGB / 30.0) * 0.001 * PUE
			dailyOps := (netKwh * gridIntensity) / 1000.0

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: "Network",
				Count: 1, DailyOps: dailyOps, DailyEmb: 0, TotalDaily: dailyOps,
			})
			totalOps += dailyOps

		// ── Lambda ────────────────────────────────────────────────────────────
		case "aws_lambda_function":
			mem := 128.0
			if v, ok := res.Values["memory_size"].(float64); ok {
				mem = v
			}
			inv := float64(lambdaInvocations)
			durH := (200.0 * inv) / 3_600_000.0
			dailyKwh := ((mem / 1024.0) * durH * x86Coeff.MinWatts * PUE) / 1000.0
			dailyOps := (dailyKwh * gridIntensity) / 1000.0

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: "Serverless",
				Count: 1, DailyOps: dailyOps, DailyEmb: 0, TotalDaily: dailyOps,
			})
			totalOps += dailyOps
		}
	}
	return impacts, totalOps, totalEmb
}

func calculateImpactWithSeries(
	plan *TFPlan,
	region string,
	intensityData []CarbonIntensityPoint,
	embodiedData map[string]float64, vcpuMap map[string]int,
	x86Coeff, armCoeff UseCoeff,
	networkProfile string, lambdaInvocations int,
	schedule UtilizationSchedule,
) ([]ResourceImpact, float64, float64, []OpsEmissionPoint) {
	const PUE = 1.135
	const LifespanDays = 1460.0

	points := normalizeIntensityData(intensityData)
	if len(points) == 0 {
		impacts, totalOps, totalEmb := calculateImpact(
			plan, region, 250.0, embodiedData, vcpuMap, x86Coeff, armCoeff, networkProfile, lambdaInvocations, schedule,
		)
		return impacts, totalOps, totalEmb, nil
	}

	opsByTimestamp := make(map[time.Time]float64)
	addSeriesOps := func(ts time.Time, ops float64) {
		if ops <= 0 {
			return
		}
		opsByTimestamp[ts] += ops
	}

	var totalOps, totalEmb float64
	var impacts []ResourceImpact

	for _, res := range plan.PlannedValues.RootModule.Resources {
		if res.Mode != "managed" {
			continue
		}

		switch res.Type {
		case "aws_instance", "aws_autoscaling_group", "aws_db_instance":
			var iType string
			count := 1.0
			var dbStorageGB float64

			switch res.Type {
			case "aws_instance":
				iType, _ = res.Values["instance_type"].(string)
			case "aws_autoscaling_group":
				if v, ok := res.Values["desired_capacity"].(float64); ok {
					count = v
				}
				iType = "t3.medium"
			case "aws_db_instance":
				if v, ok := res.Values["instance_class"].(string); ok {
					iType = strings.TrimPrefix(v, "db.")
				}
				if v, ok := res.Values["allocated_storage"].(float64); ok {
					dbStorageGB = v
				}
			}
			if iType == "" {
				iType = "m5.large"
			}

			vcpus := float64(getVCPUs(iType, vcpuMap))
			coeff := x86Coeff
			if strings.Contains(iType, "r7g") || strings.HasSuffix(strings.Split(iType, ".")[0], "g") {
				coeff = armCoeff
			}

			var dailyOps float64
			for i, p := range points {
				hours := seriesIntervalHours(points, i)
				if hours <= 0 {
					continue
				}
				utilization := utilizationAtTime(schedule, p.Timestamp)
				avgW := coeff.MinWatts + (coeff.MaxWatts-coeff.MinWatts)*utilization
				kwh := (avgW * vcpus * hours * PUE) / 1000.0
				opsKg := (kwh * p.Intensity / 1000.0) * count
				dailyOps += opsKg
				addSeriesOps(p.Timestamp, opsKg)

				if res.Type == "aws_db_instance" && dbStorageGB > 0 {
					sKwh := (0.0012 * dbStorageGB * hours * PUE) / 1000.0
					sOps := (sKwh * p.Intensity / 1000.0) * count
					dailyOps += sOps
					addSeriesOps(p.Timestamp, sOps)
				}

				if res.Type == "aws_instance" || res.Type == "aws_autoscaling_group" {
					trafficGB := (trafficByProfile(networkProfile) / 30.0 / 24.0) * hours
					netKwh := trafficGB * 0.001 * PUE
					netOps := (netKwh * p.Intensity / 1000.0) * count
					dailyOps += netOps
					addSeriesOps(p.Timestamp, netOps)
				}
			}

			emb := embodiedData[iType]
			if emb == 0 {
				emb = 1200.0
			}
			dailyEmb := (emb / LifespanDays) * count
			if res.Type == "aws_db_instance" && dbStorageGB > 0 {
				dailyEmb += ((dbStorageGB / 1024.0) * 50.0 / LifespanDays) * count
			}

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: iType,
				Count: int(count), DailyOps: dailyOps, DailyEmb: dailyEmb, TotalDaily: dailyOps + dailyEmb,
			})
			totalOps += dailyOps
			totalEmb += dailyEmb

		case "aws_ebs_volume":
			var size float64
			if v, ok := res.Values["size"].(float64); ok {
				size = v
			}
			vType, _ := res.Values["type"].(string)
			isSSD := vType == "gp2" || vType == "gp3" || vType == "io1" || vType == "io2" || vType == ""
			wPerGB, embPerTB := 0.0065, 20.0
			if isSSD {
				wPerGB, embPerTB = 0.0012, 50.0
			}

			var dailyOps float64
			for i, p := range points {
				hours := seriesIntervalHours(points, i)
				if hours <= 0 {
					continue
				}
				kwh := (wPerGB * size * hours * PUE) / 1000.0
				opsKg := (kwh * p.Intensity) / 1000.0
				dailyOps += opsKg
				addSeriesOps(p.Timestamp, opsKg)
			}
			dailyEmb := (size / 1024.0) * embPerTB / LifespanDays

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: "Storage",
				Count: 1, DailyOps: dailyOps, DailyEmb: dailyEmb, TotalDaily: dailyOps + dailyEmb,
			})
			totalOps += dailyOps
			totalEmb += dailyEmb

		case "aws_lb", "aws_alb", "aws_elb":
			var dailyOps float64
			for i, p := range points {
				hours := seriesIntervalHours(points, i)
				if hours <= 0 {
					continue
				}
				trafficGB := (trafficByProfile(networkProfile) / 30.0 / 24.0) * hours
				netKwh := trafficGB * 0.001 * PUE
				opsKg := (netKwh * p.Intensity) / 1000.0
				dailyOps += opsKg
				addSeriesOps(p.Timestamp, opsKg)
			}

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: "Network",
				Count: 1, DailyOps: dailyOps, DailyEmb: 0, TotalDaily: dailyOps,
			})
			totalOps += dailyOps

		case "aws_lambda_function":
			mem := 128.0
			if v, ok := res.Values["memory_size"].(float64); ok {
				mem = v
			}
			inv := float64(lambdaInvocations)
			dayDurHours := (200.0 * inv) / 3_600_000.0

			var dailyOps float64
			for i, p := range points {
				hours := seriesIntervalHours(points, i)
				if hours <= 0 {
					continue
				}
				durHours := (dayDurHours / 24.0) * hours
				kwh := ((mem / 1024.0) * durHours * x86Coeff.MinWatts * PUE) / 1000.0
				opsKg := (kwh * p.Intensity) / 1000.0
				dailyOps += opsKg
				addSeriesOps(p.Timestamp, opsKg)
			}

			impacts = append(impacts, ResourceImpact{
				Type: res.Type, Name: res.Name, Instance: "Serverless",
				Count: 1, DailyOps: dailyOps, DailyEmb: 0, TotalDaily: dailyOps,
			})
			totalOps += dailyOps
		}
	}

	opsSeries := make([]OpsEmissionPoint, 0, len(opsByTimestamp))
	for ts, emissions := range opsByTimestamp {
		opsSeries = append(opsSeries, OpsEmissionPoint{Timestamp: ts, Emissions: emissions})
	}
	sort.Slice(opsSeries, func(i, j int) bool {
		return opsSeries[i].Timestamp.Before(opsSeries[j].Timestamp)
	})

	return impacts, totalOps, totalEmb, opsSeries
}

func normalizeIntensityData(points []CarbonIntensityPoint) []CarbonIntensityPoint {
	out := make([]CarbonIntensityPoint, 0, len(points))
	for _, p := range points {
		if p.Intensity <= 0 || p.Timestamp.IsZero() {
			continue
		}
		out = append(out, CarbonIntensityPoint{
			Timestamp: p.Timestamp.UTC(),
			Intensity: p.Intensity,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Timestamp.Before(out[j].Timestamp)
	})
	dedup := make([]CarbonIntensityPoint, 0, len(out))
	for _, p := range out {
		if len(dedup) > 0 && dedup[len(dedup)-1].Timestamp.Equal(p.Timestamp) {
			dedup[len(dedup)-1] = p
			continue
		}
		dedup = append(dedup, p)
	}
	return dedup
}

func seriesIntervalHours(points []CarbonIntensityPoint, idx int) float64 {
	if idx < 0 || idx >= len(points) {
		return 0
	}
	if idx < len(points)-1 {
		h := points[idx+1].Timestamp.Sub(points[idx].Timestamp).Hours()
		if h > 0 {
			return h
		}
	}
	if idx > 0 {
		h := points[idx].Timestamp.Sub(points[idx-1].Timestamp).Hours()
		if h > 0 {
			return h
		}
	}
	return 1.0
}

func utilizationAtTime(s UtilizationSchedule, ts time.Time) float64 {
	s = normalizeSchedule(s)
	hour := ts.UTC().Hour()
	work := false
	if s.WorkStartHour == s.WorkEndHour {
		work = true
	} else if s.WorkStartHour < s.WorkEndHour {
		work = hour >= s.WorkStartHour && hour < s.WorkEndHour
	} else {
		work = hour >= s.WorkStartHour || hour < s.WorkEndHour
	}
	if work {
		return s.WorkPct / 100.0
	}
	return s.IdlePct / 100.0
}

func normalizeSchedule(s UtilizationSchedule) UtilizationSchedule {
	normalized := UtilizationSchedule{
		WorkStartHour: s.WorkStartHour,
		WorkEndHour:   s.WorkEndHour,
		WorkPct:       s.WorkPct,
		IdlePct:       s.IdlePct,
	}
	if normalized.WorkStartHour < 0 || normalized.WorkStartHour > 23 {
		normalized.WorkStartHour = 0
	}
	if normalized.WorkEndHour < 0 || normalized.WorkEndHour > 24 {
		normalized.WorkEndHour = 24
	}
	if normalized.WorkPct < 0 || normalized.WorkPct > 100 {
		normalized.WorkPct = 50
	}
	if normalized.IdlePct < 0 || normalized.IdlePct > 100 {
		normalized.IdlePct = 50
	}
	return normalized
}

func scheduleUtilization(s UtilizationSchedule) float64 {
	s = normalizeSchedule(s)
	workHours := scheduleWorkHours(s.WorkStartHour, s.WorkEndHour)
	idleHours := 24 - workHours
	weighted := (float64(workHours)*s.WorkPct + float64(idleHours)*s.IdlePct) / (24.0 * 100.0)
	if weighted < 0 {
		return 0
	}
	if weighted > 1 {
		return 1
	}
	return weighted
}

func scheduleWorkHours(start, end int) int {
	if start < 0 {
		start = 0
	}
	if start > 23 {
		start = 23
	}
	if end < 0 {
		end = 0
	}
	if end > 24 {
		end = 24
	}
	if end == start {
		return 24
	}
	if end > start {
		return end - start
	}
	// Wrap-around window, e.g. 22 -> 6.
	return (24 - start) + end
}

func trafficByProfile(profile string) float64 {
	switch profile {
	case "low":
		return 10.0
	case "high":
		return 1000.0
	default:
		return 100.0
	}
}

// ── Graviton simulation ───────────────────────────────────────────────────────

// simulateGraviton returns a shallow copy of plan with common x86 instance
// families swapped for their Graviton equivalents.
func simulateGraviton(plan TFPlan) TFPlan {
	for i, res := range plan.PlannedValues.RootModule.Resources {
		if res.Mode != "managed" {
			continue
		}
		switch res.Type {
		case "aws_instance":
			if v, ok := res.Values["instance_type"].(string); ok {
				plan.PlannedValues.RootModule.Resources[i].Values["instance_type"] = gravitonVariant(v)
			}
		case "aws_db_instance":
			if v, ok := res.Values["instance_class"].(string); ok {
				plan.PlannedValues.RootModule.Resources[i].Values["instance_class"] = gravitonVariant(v)
			}
		}
	}
	return plan
}

func gravitonVariant(v string) string {
	for old, neu := range map[string]string{
		"m5.": "m6g.", "m6i.": "m6g.", "m7i.": "m7g.",
		"t3.": "t4g.", "t3a.": "t4g.",
		"c5.": "c7g.", "c6i.": "c7g.",
		"r5.": "r7g.", "r6i.": "r7g.",
	} {
		if strings.Contains(v, old) {
			return strings.Replace(v, old, neu, 1)
		}
	}
	return v
}
