package main

import (
	"encoding/json"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

const (
	monthlyHours      = 730.0
	pricingAPIRegion  = "ap-south-1"
	defaultS3StorageG = 1.0
	fixedEIPHourlyUSD = 0.005
)

var hiddenFreeResourceTypes = map[string]bool{
	"aws_autoscaling_policy":         true,
	"aws_db_subnet_group":            true,
	"aws_eip_association":            true,
	"aws_iam_instance_profile":       true,
	"aws_iam_role":                   true,
	"aws_iam_role_policy":            true,
	"aws_iam_role_policy_attachment": true,
	"aws_internet_gateway":           true,
	"aws_lb_listener":                true,
	"aws_lb":                         true,
	"aws_alb":                        true,
	"aws_elb":                        true,
	"aws_lb_target_group":            true,
	"aws_route":                      true,
	"aws_route_table":                true,
	"aws_route_table_association":    true,
	"aws_security_group":             true,
	"aws_ssm_parameter":              true,
	"aws_subnet":                     true,
	"aws_vpc":                        true,
}

type pricingClient struct {
	cache map[string]float64
}

func buildCostSummary(plan *TFPlan, regionCode string, lambdaInvocations int) (CostSummary, string) {
	summary := CostSummary{Region: regionCode}
	if _, err := exec.LookPath("aws"); err != nil {
		return summary, "AWS CLI not found; pricing comparison is unavailable."
	}

	client := &pricingClient{cache: make(map[string]float64)}
	seenWarnings := make(map[string]bool)
	var warnings []string

	addWarning := func(msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" || seenWarnings[msg] {
			return
		}
		seenWarnings[msg] = true
		warnings = append(warnings, msg)
	}

	for _, res := range plan.PlannedValues.RootModule.Resources {
		if res.Mode != "managed" || hiddenFreeResourceTypes[res.Type] {
			continue
		}

		row := CostResource{
			Address:   res.Address,
			Type:      res.Type,
			Name:      res.Name,
			Quantity:  1,
			Unit:      "Hrs",
			Available: false,
			Note:      "price unavailable",
		}

		switch res.Type {
		case "aws_instance":
			instanceType := stringOrDefault(res.Values["instance_type"], "m5.large")
			row.Note = "On-Demand Linux shared tenancy"
			row.Hourly, row.Available, row.Note = lookupEC2Hourly(client, regionCode, instanceType)
			row.Monthly = row.Hourly * monthlyHours

		case "aws_autoscaling_group":
			desired := floatOrDefault(res.Values["desired_capacity"], 1)
			row.Quantity = desired
			instanceType := "t3.medium"
			row.Note = "assumes t3.medium for ASG capacity"
			unitHourly, okPrice, note := lookupEC2Hourly(client, regionCode, instanceType)
			row.Available = okPrice
			if okPrice {
				row.Hourly = unitHourly * desired
				row.Monthly = row.Hourly * monthlyHours
			}
			if note != "" {
				row.Note = note
			}

		case "aws_db_instance":
			class := stringOrDefault(res.Values["instance_class"], "db.t3.medium")
			engine := normalizeRDSEngine(stringOrDefault(res.Values["engine"], "mysql"))
			row.Note = "On-Demand DB instance + allocated storage estimate"
			instanceHourly, okInst, noteInst := lookupRDSInstanceHourly(client, regionCode, class, engine)
			storageGB := floatOrDefault(res.Values["allocated_storage"], 0)
			storageType := normalizeRDSStorageType(stringOrDefault(res.Values["storage_type"], "gp2"))
			storageMonthlyPerGB, okStorage, noteStorage := lookupRDSStorageMonthlyPerGB(client, regionCode, storageType)

			if okInst {
				row.Hourly += instanceHourly
				row.Monthly += instanceHourly * monthlyHours
			}
			if okStorage && storageGB > 0 {
				row.Monthly += storageMonthlyPerGB * storageGB
				row.Hourly += (storageMonthlyPerGB * storageGB) / monthlyHours
			}
			row.Available = okInst || (okStorage && storageGB > 0)
			if !okInst && !okStorage {
				row.Note = firstNonEmpty(noteStorage, noteInst)
			} else if !okInst {
				row.Note = "instance price unavailable; storage estimated"
			} else if storageGB > 0 && !okStorage {
				row.Note = "storage price unavailable; instance estimated"
			}

		case "aws_ebs_volume":
			sizeGB := floatOrDefault(res.Values["size"], 0)
			row.Quantity = sizeGB
			row.Unit = "GB-Mo"
			volType := normalizeEBSVolumeType(stringOrDefault(res.Values["type"], "gp3"))
			monthlyPerGB, okPrice, note := lookupEBSMonthlyPerGB(client, regionCode, volType)
			row.Available = okPrice
			if okPrice {
				row.Monthly = monthlyPerGB * sizeGB
				row.Hourly = row.Monthly / monthlyHours
				row.Note = "EBS storage estimate"
			} else {
				row.Note = note
			}

		case "aws_nat_gateway":
			row.Note = "NAT gateway hourly estimate"
			row.Hourly, row.Available, row.Note = lookupNatGatewayHourly(client, regionCode)
			row.Monthly = row.Hourly * monthlyHours

		case "aws_eip":
			row.Note = "fixed Elastic IP hourly estimate"
			row.Hourly = fixedEIPHourlyUSD
			row.Available = true
			row.Monthly = row.Hourly * monthlyHours

		case "aws_s3_bucket":
			row.Unit = "GB-Mo"
			row.Quantity = defaultS3StorageG
			row.Note = "S3 Standard storage estimate (assumes 1 GB-month)"
			monthlyPerGB, okPrice, note := lookupS3StandardMonthlyPerGB(client, regionCode)
			row.Available = okPrice
			if okPrice {
				row.Monthly = monthlyPerGB * row.Quantity
				row.Hourly = row.Monthly / monthlyHours
			} else {
				row.Note = note
			}

		case "aws_lambda_function":
			row.Note = "Lambda request + duration estimate"
			memMB := floatOrDefault(res.Values["memory_size"], 128)
			durationSec := 0.2
			hourlyInvocations := float64(lambdaInvocations) / 24.0
			hourlyGBSeconds := (memMB / 1024.0) * durationSec * hourlyInvocations
			hourlyMillions := hourlyInvocations / 1_000_000.0

			gbSecondPrice, okDuration, noteDuration := lookupLambdaDurationPerGBSecond(client, regionCode)
			requestPrice, okRequests, noteRequests := lookupLambdaRequestsPerMillion(client, regionCode)
			if okDuration {
				row.Hourly += hourlyGBSeconds * gbSecondPrice
			}
			if okRequests {
				row.Hourly += hourlyMillions * requestPrice
			}
			row.Monthly = row.Hourly * monthlyHours
			row.Available = okDuration || okRequests
			if !okDuration && !okRequests {
				row.Note = firstNonEmpty(noteRequests, noteDuration)
			} else if !okDuration {
				row.Note = "duration price unavailable; request estimate only"
			} else if !okRequests {
				row.Note = "request price unavailable; duration estimate only"
			}

		default:
			row.Note = "pricing mapping not implemented"
		}

		if !row.Available {
			summary.UnavailableCount++
		} else {
			summary.TotalHourly += row.Hourly
			summary.TotalMonthly += row.Monthly
		}
		if res.Type != "aws_lambda_function" && strings.Contains(row.Note, "AWS CLI query failed") {
			addWarning(row.Note)
		}
		summary.Resources = append(summary.Resources, row)
	}

	if len(warnings) == 0 {
		return summary, ""
	}
	sort.Strings(warnings)
	return summary, strings.Join(warnings, "\n")
}

func lookupEC2Hourly(client *pricingClient, regionCode, instanceType string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonEC2", regionCode, map[string]string{
		"capacitystatus":  "Used",
		"instanceType":    instanceType,
		"operatingSystem": "Linux",
		"preInstalledSw":  "NA",
		"tenancy":         "Shared",
	}, []string{"Hrs"}, nil)
	if err != nil {
		return 0, false, "AWS CLI query failed for EC2: " + err.Error()
	}
	return price, true, "On-Demand Linux shared tenancy"
}

func lookupRDSInstanceHourly(client *pricingClient, regionCode, instanceType, engine string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonRDS", regionCode, map[string]string{
		"databaseEngine": engine,
		"instanceType":   instanceType,
	}, []string{"Hrs"}, nil)
	if err != nil {
		return 0, false, "AWS CLI query failed for RDS instance: " + err.Error()
	}
	return price, true, ""
}

func lookupRDSStorageMonthlyPerGB(client *pricingClient, regionCode, storageType string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonRDS", regionCode, map[string]string{
		"volumeType": storageType,
	}, []string{"GB-Mo"}, []string{"Storage"})
	if err != nil {
		return 0, false, "AWS CLI query failed for RDS storage: " + err.Error()
	}
	return price, true, ""
}

func lookupEBSMonthlyPerGB(client *pricingClient, regionCode, volumeType string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonEC2", regionCode, map[string]string{
		"productFamily": "Storage",
		"volumeType":    volumeType,
	}, []string{"GB-Mo"}, nil)
	if err != nil {
		return 0, false, "AWS CLI query failed for EBS: " + err.Error()
	}
	return price, true, ""
}

func lookupNatGatewayHourly(client *pricingClient, regionCode string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonEC2", regionCode, map[string]string{
		"productFamily": "NAT Gateway",
	}, []string{"Hrs"}, nil)
	if err != nil {
		return 0, false, "AWS CLI query failed for NAT Gateway: " + err.Error()
	}
	return price, true, ""
}

func lookupLoadBalancerHourly(client *pricingClient, regionCode, lbType string) (float64, bool, string) {
	descHints := []string{"LoadBalancerUsage", "Load Balancer Usage"}
	switch lbType {
	case "network":
		descHints = append(descHints, "Network")
	case "gateway":
		descHints = append(descHints, "Gateway")
	default:
		descHints = append(descHints, "Application")
	}
	price, err := client.lookupPrice("AWSElasticLoadBalancing", regionCode, map[string]string{
		"productFamily": "Load Balancer",
	}, []string{"Hrs"}, descHints)
	if err != nil {
		return 0, false, "AWS CLI query failed for Load Balancer: " + err.Error()
	}
	return price, true, ""
}

func lookupEIPHourly(client *pricingClient, regionCode string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonEC2", regionCode, map[string]string{
		"productFamily": "IP Address",
	}, []string{"Hrs"}, []string{"ElasticIP", "In-use", "IP Address"})
	if err != nil {
		return 0, false, "AWS CLI query failed for Elastic IP: " + err.Error()
	}
	return price, true, ""
}

func lookupS3StandardMonthlyPerGB(client *pricingClient, regionCode string) (float64, bool, string) {
	price, err := client.lookupPrice("AmazonS3", regionCode, map[string]string{
		"productFamily": "Storage",
		"volumeType":    "Standard",
	}, []string{"GB-Mo"}, []string{"TimedStorage", "Standard"})
	if err != nil {
		return 0, false, "AWS CLI query failed for S3: " + err.Error()
	}
	return price, true, ""
}

func lookupLambdaDurationPerGBSecond(client *pricingClient, regionCode string) (float64, bool, string) {
	price, err := client.lookupPrice("AWSLambda", regionCode, map[string]string{
		"group": "AWS-Lambda-Duration",
	}, []string{"GB-Second"}, nil)
	if err != nil {
		// Fallback for regions/accounts where group labels differ in the catalog.
		price, fallbackErr := client.lookupPrice("AWSLambda", regionCode, map[string]string{}, []string{"GB-Second"}, []string{"duration", "lambda"})
		if fallbackErr != nil {
			return 0, false, "AWS CLI query failed for Lambda duration: " + err.Error()
		}
		return price, true, ""
	}
	return price, true, ""
}

func lookupLambdaRequestsPerMillion(client *pricingClient, regionCode string) (float64, bool, string) {
	price, err := client.lookupPrice("AWSLambda", regionCode, map[string]string{
		"group": "AWS-Lambda-Requests",
	}, []string{"1M Requests"}, nil)
	if err != nil {
		// Fallback for regions/accounts where group labels differ in the catalog.
		price, fallbackErr := client.lookupPrice("AWSLambda", regionCode, map[string]string{}, []string{"1M Requests"}, []string{"request", "lambda"})
		if fallbackErr != nil {
			return 0, false, "AWS CLI query failed for Lambda requests: " + err.Error()
		}
		return price, true, ""
	}
	return price, true, ""
}

func (c *pricingClient) lookupPrice(
	serviceCode, regionCode string,
	filters map[string]string,
	preferredUnits []string,
	descriptionContains []string,
) (float64, error) {
	merged := map[string]string{"regionCode": regionCode}
	for k, v := range filters {
		merged[k] = v
	}

	var keys []string
	for k := range merged {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	cacheKey := serviceCode + "::endpoint=" + pricingAPIRegion + ";"
	for _, k := range keys {
		cacheKey += k + "=" + merged[k] + ";"
	}
	cacheKey += strings.Join(preferredUnits, ",") + "|" + strings.Join(descriptionContains, ",")
	if v, ok := c.cache[cacheKey]; ok {
		return v, nil
	}

	args := []string{
		"pricing", "get-products",
		"--service-code", serviceCode,
		"--region", pricingAPIRegion,
		"--max-results", "100",
		"--output", "json",
	}
	for _, k := range keys {
		args = append(args, "--filters", fmt.Sprintf("Type=TERM_MATCH,Field=%s,Value=%s", k, merged[k]))
	}

	cmd := exec.Command("aws", args...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return 0, fmt.Errorf("%s", strings.TrimSpace(string(ee.Stderr)))
		}
		return 0, err
	}

	var payload struct {
		PriceList []string `json:"PriceList"`
	}
	if err := json.Unmarshal(out, &payload); err != nil {
		return 0, fmt.Errorf("cannot parse pricing output")
	}
	if len(payload.PriceList) == 0 {
		return 0, fmt.Errorf("no pricing entries")
	}
	for _, item := range payload.PriceList {
		if price, ok := extractOnDemandPrice(item, preferredUnits, descriptionContains); ok {
			c.cache[cacheKey] = price
			return price, nil
		}
	}
	return 0, fmt.Errorf("no on-demand price dimension matched")
}

func extractOnDemandPrice(raw string, preferredUnits, descriptionContains []string) (float64, bool) {
	var doc struct {
		Terms struct {
			OnDemand map[string]struct {
				PriceDimensions map[string]struct {
					Unit         string            `json:"unit"`
					Description  string            `json:"description"`
					PricePerUnit map[string]string `json:"pricePerUnit"`
				} `json:"priceDimensions"`
			} `json:"OnDemand"`
		} `json:"terms"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return 0, false
	}

	for _, term := range doc.Terms.OnDemand {
		for _, dim := range term.PriceDimensions {
			if !matchAny(dim.Unit, preferredUnits) {
				continue
			}
			if !containsAny(strings.ToLower(dim.Description), descriptionContains) {
				continue
			}
			usd := strings.TrimSpace(dim.PricePerUnit["USD"])
			if usd == "" {
				continue
			}
			v, err := strconv.ParseFloat(usd, 64)
			if err == nil {
				return v, true
			}
		}
	}
	return 0, false
}

func matchAny(value string, expected []string) bool {
	if len(expected) == 0 {
		return true
	}
	for _, e := range expected {
		if value == e {
			return true
		}
	}
	return false
}

func containsAny(value string, hints []string) bool {
	if len(hints) == 0 {
		return true
	}
	for _, h := range hints {
		if strings.Contains(value, strings.ToLower(h)) {
			return true
		}
	}
	return false
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func stringOrDefault(v interface{}, fallback string) string {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func floatOrDefault(v interface{}, fallback float64) float64 {
	f, ok := v.(float64)
	if !ok {
		return fallback
	}
	return f
}

func normalizeRDSEngine(engine string) string {
	switch strings.ToLower(strings.TrimSpace(engine)) {
	case "postgres", "postgresql":
		return "PostgreSQL"
	case "mariadb":
		return "MariaDB"
	case "aurora-mysql", "aurora":
		return "Aurora MySQL"
	case "aurora-postgresql":
		return "Aurora PostgreSQL"
	case "oracle-ee":
		return "Oracle"
	case "sqlserver-ex":
		return "SQL Server"
	default:
		return "MySQL"
	}
}

func normalizeRDSStorageType(storage string) string {
	switch strings.ToLower(strings.TrimSpace(storage)) {
	case "gp3":
		return "General Purpose-GP3"
	case "io1":
		return "Provisioned IOPS"
	case "io2":
		return "Provisioned IOPS-IO2"
	default:
		return "General Purpose"
	}
}

func normalizeEBSVolumeType(volumeType string) string {
	switch strings.ToLower(strings.TrimSpace(volumeType)) {
	case "gp2", "gp3":
		return "General Purpose"
	case "io1":
		return "Provisioned IOPS"
	case "io2":
		return "Provisioned IOPS-io2"
	case "st1":
		return "Throughput Optimized HDD"
	case "sc1":
		return "Cold HDD"
	default:
		return "General Purpose"
	}
}
