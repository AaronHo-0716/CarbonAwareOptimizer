package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const (
	monthlyHours      = 730.0
	pricingAPIRegion  = "ap-south-1"
	defaultS3StorageG = 1.0
	fixedEIPHourlyUSD = 0.005
	pricingCacheDir   = ".cache/aws-pricing"
)

var hiddenFreeResourceTypes = map[string]bool{
	"aws_autoscaling_policy":            true,
	"aws_db_subnet_group":               true,
	"aws_eip_association":               true,
	"aws_iam_instance_profile":          true,
	"aws_iam_role":                      true,
	"aws_iam_role_policy":               true,
	"aws_iam_role_policy_attachment":    true,
	"aws_internet_gateway":              true,
	"aws_launch_template":               true,
	"aws_lb_listener":                   true,
	"aws_lb":                            true,
	"aws_alb":                           true,
	"aws_elb":                           true,
	"aws_lb_target_group":               true,
	"aws_route":                         true,
	"aws_route_table":                   true,
	"aws_route_table_association":       true,
	"aws_s3_bucket_cors_configuration":  true,
	"aws_s3_bucket_public_access_block": true,
	"aws_security_group":                true,
	"aws_ssm_parameter":                 true,
	"aws_subnet":                        true,
	"aws_vpc":                           true,
}

// ─── Core Pricing Structures ─────────────────────────────────────────────────

type pricingClient struct {
	regionCode string
	cache      map[string]*awsPriceResponse
}

type awsPriceResponse struct {
	FormatVersion string   `json:"FormatVersion"`
	PriceList     []string `json:"PriceList"`
}

type awsProduct struct {
	Product struct {
		ProductFamily string            `json:"productFamily"`
		Attributes    map[string]string `json:"attributes"`
	} `json:"product"`
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

// ─── Pricing Client ──────────────────────────────────────────────────────────

func newPricingClient(regionCode string) *pricingClient {
	return &pricingClient{
		regionCode: regionCode,
		cache:      make(map[string]*awsPriceResponse),
	}
}

// queryAWSPricing makes a call to AWS Pricing API and caches the result
func (c *pricingClient) queryAWSPricing(serviceCode string, filters map[string]string) (*awsPriceResponse, error) {
	// Build cache key from service and filters
	cacheKey := buildCacheKey(serviceCode, filters)

	// Check cache first
	if cached, ok := c.cache[cacheKey]; ok {
		return cached, nil
	}

	// Check file cache
	cacheFile := filepath.Join(pricingCacheDir, cacheKey+".json")
	if data, err := os.ReadFile(cacheFile); err == nil {
		var response awsPriceResponse
		if json.Unmarshal(data, &response) == nil && len(response.PriceList) > 0 {
			c.cache[cacheKey] = &response
			return &response, nil
		}
	}

	// Build AWS CLI command
	args := []string{
		"pricing", "get-products",
		"--service-code", serviceCode,
		"--region", pricingAPIRegion,
		"--max-results", "10",
		"--output", "json",
	}

	// Add filters. AWS CLI's --filters is a list parameter: all entries must
	// follow a single --filters flag as shorthand list items, otherwise only
	// one (effectively random) filter is honored.
	if len(filters) > 0 {
		args = append(args, "--filters")
		for key, value := range filters {
			args = append(args, fmt.Sprintf("Type=TERM_MATCH,Field=%s,Value=%s", key, value))
		}
	}

	// Execute AWS CLI
	cmd := exec.Command("aws", args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("AWS CLI error: %s", string(exitErr.Stderr))
		}
		return nil, fmt.Errorf("failed to execute AWS CLI: %w", err)
	}

	// Parse response
	var response awsPriceResponse
	if err := json.Unmarshal(output, &response); err != nil {
		return nil, fmt.Errorf("failed to parse AWS response: %w", err)
	}

	if len(response.PriceList) == 0 {
		return nil, fmt.Errorf("no pricing data found")
	}

	// Cache to memory and disk
	c.cache[cacheKey] = &response
	os.MkdirAll(pricingCacheDir, 0755)
	if data, err := json.Marshal(response); err == nil {
		os.WriteFile(cacheFile, data, 0644)
	}

	return &response, nil
}

// extractPrice finds the cheapest positive OnDemand price with the given unit.
// Relies entirely on the API filters being tight — no attribute re-checking needed.
func (c *pricingClient) extractPrice(response *awsPriceResponse, unit string) (float64, error) {
	var lowestPrice float64 = -1

	for _, priceItemJSON := range response.PriceList {
		var product awsProduct
		if err := json.Unmarshal([]byte(priceItemJSON), &product); err != nil {
			continue
		}

		for _, term := range product.Terms.OnDemand {
			for _, dimension := range term.PriceDimensions {
				if dimension.Unit != unit {
					continue
				}
				priceStr := strings.TrimSpace(dimension.PricePerUnit["USD"])
				if priceStr == "" {
					continue
				}
				price, err := strconv.ParseFloat(priceStr, 64)
				if err != nil || price <= 0 {
					continue
				}
				if lowestPrice < 0 || price < lowestPrice {
					lowestPrice = price
				}
			}
		}
	}

	if lowestPrice < 0 {
		return 0, fmt.Errorf("no OnDemand price found for unit %q", unit)
	}

	return lowestPrice, nil
}

// ─── Specific Pricing Lookups ────────────────────────────────────────────────

func (c *pricingClient) getEC2Price(instanceType string) (float64, error) {
	response, err := c.queryAWSPricing("AmazonEC2", map[string]string{
		"regionCode":      c.regionCode,
		"productFamily":   "Compute Instance",
		"instanceType":    instanceType,
		"operatingSystem": "Linux",
		"tenancy":         "Shared",
		"preInstalledSw":  "NA",
		"capacitystatus":  "Used",
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "Hrs")
}

func (c *pricingClient) getRDSPrice(instanceType, engine string, multiAZ bool) (float64, error) {
	deploymentOption := "Single-AZ"
	if multiAZ {
		deploymentOption = "Multi-AZ"
	}

	response, err := c.queryAWSPricing("AmazonRDS", map[string]string{
		"regionCode":       c.regionCode,
		"productFamily":    "Database Instance",
		"instanceType":     instanceType,
		"databaseEngine":   normalizeRDSEngine(engine),
		"deploymentOption": deploymentOption,
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "Hrs")
}

func (c *pricingClient) getRDSStoragePrice(storageType, engine string) (float64, error) {
	response, err := c.queryAWSPricing("AmazonRDS", map[string]string{
		"regionCode":     c.regionCode,
		"productFamily":  "Database Storage",
		"volumeType":     normalizeRDSStorageType(storageType),
		"databaseEngine": normalizeRDSEngine(engine),
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "GB-Mo")
}

func (c *pricingClient) getEBSPrice(volumeType string) (float64, error) {
	response, err := c.queryAWSPricing("AmazonEC2", map[string]string{
		"regionCode":    c.regionCode,
		"productFamily": "Storage",
		"volumeType":    normalizeEBSVolumeType(volumeType),
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "GB-Mo")
}

func (c *pricingClient) getNATGatewayPrice() (float64, error) {
	response, err := c.queryAWSPricing("AmazonEC2", map[string]string{
		"regionCode":    c.regionCode,
		"productFamily": "NAT Gateway",
		"group":         "NGW:NatGateway",
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "Hrs")
}

func (c *pricingClient) getS3Price() (float64, error) {
	response, err := c.queryAWSPricing("AmazonS3", map[string]string{
		"regionCode":    c.regionCode,
		"productFamily": "Storage",
		"volumeType":    "Standard",
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "GB-Mo")
}

func (c *pricingClient) getLambdaDurationPrice() (float64, error) {
	response, err := c.queryAWSPricing("AWSLambda", map[string]string{
		"regionCode": c.regionCode,
		"group":      "AWS-Lambda-Duration",
	})
	if err != nil {
		return 0, err
	}
	return c.extractPrice(response, "Lambda-GB-Second")
}

func (c *pricingClient) getLambdaRequestPrice() (float64, error) {
	response, err := c.queryAWSPricing("AWSLambda", map[string]string{
		"regionCode": c.regionCode,
		"group":      "AWS-Lambda-Requests",
	})
	if err != nil {
		return 0, err
	}
	price, err := c.extractPrice(response, "Request")
	if err != nil {
		return 0, err
	}
	// Price is per-request; multiply by 1M for per-million rate used in calc
	return price * 1_000_000, nil
}

// ─── Build Cost Summary ──────────────────────────────────────────────────────

func buildCostSummary(plan *TFPlan, regionCode string, lambdaInvocations int) (CostSummary, string) {
	summary := CostSummary{Region: regionCode}

	// Check if AWS CLI is available
	if _, err := exec.LookPath("aws"); err != nil {
		return summary, "AWS CLI not found; pricing comparison is unavailable."
	}

	client := newPricingClient(regionCode)
	var warnings []string
	seenWarnings := make(map[string]bool)

	addWarning := func(msg string) {
		msg = strings.TrimSpace(msg)
		if msg == "" || seenWarnings[msg] {
			return
		}
		seenWarnings[msg] = true
		warnings = append(warnings, msg)
	}

	// Get launch template instance types for ASGs
	ltInstanceByAddress := launchTemplateInstanceTypes(plan)

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
			instanceType := getStringValue(res.Values, "instance_type", "m5.large")
			if price, err := client.getEC2Price(instanceType); err == nil {
				row.Hourly = price
				row.Monthly = price * monthlyHours
				row.Available = true
				row.Note = "On-Demand Linux"
			} else {
				row.Note = fmt.Sprintf("EC2 pricing error: %s", err.Error())
				addWarning(row.Note)
			}

		case "aws_autoscaling_group":
			desired := getFloatValue(res.Values, "desired_capacity", 1)
			row.Quantity = desired
			instanceType := resolveASGInstanceType(plan, res.Address, res.Values, ltInstanceByAddress)

			if price, err := client.getEC2Price(instanceType); err == nil {
				row.Hourly = price * desired
				row.Monthly = price * desired * monthlyHours
				row.Available = true
				row.Note = fmt.Sprintf("On-Demand Linux (%s)", instanceType)
			} else {
				row.Note = fmt.Sprintf("EC2 pricing error: %s", err.Error())
				addWarning(row.Note)
			}

		case "aws_db_instance":
			instanceClass := getStringValue(res.Values, "instance_class", "db.t3.medium")
			engine := getStringValue(res.Values, "engine", "mysql")
			multiAZ := getBoolValue(res.Values, "multi_az", false)
			storageGB := getFloatValue(res.Values, "allocated_storage", 0)
			storageType := getStringValue(res.Values, "storage_type", "gp2")

			// Get instance price
			if price, err := client.getRDSPrice(instanceClass, engine, multiAZ); err == nil {
				row.Hourly += price
				row.Monthly += price * monthlyHours
				row.Available = true
			} else {
				row.Note = fmt.Sprintf("RDS instance pricing error: %s", err.Error())
				addWarning(row.Note)
			}

			// Get storage price
			if storageGB > 0 {
				if price, err := client.getRDSStoragePrice(storageType, engine); err == nil {
					row.Monthly += price * storageGB
					row.Hourly += (price * storageGB) / monthlyHours
					row.Available = true
				}
			}

			if row.Available {
				row.Note = "On-Demand RDS instance + storage"
			}

		case "aws_ebs_volume":
			sizeGB := getFloatValue(res.Values, "size", 0)
			volumeType := getStringValue(res.Values, "type", "gp3")
			row.Quantity = sizeGB
			row.Unit = "GB-Mo"

			if price, err := client.getEBSPrice(volumeType); err == nil {
				row.Monthly = price * sizeGB
				row.Hourly = row.Monthly / monthlyHours
				row.Available = true
				row.Note = "EBS storage"
			} else {
				row.Note = fmt.Sprintf("EBS pricing error: %s", err.Error())
				addWarning(row.Note)
			}

		case "aws_nat_gateway":
			if price, err := client.getNATGatewayPrice(); err == nil {
				row.Hourly = price
				row.Monthly = price * monthlyHours
				row.Available = true
				row.Note = "NAT Gateway hourly"
			} else {
				row.Note = fmt.Sprintf("NAT Gateway pricing error: %s", err.Error())
				addWarning(row.Note)
			}

		case "aws_eip":
			row.Hourly = fixedEIPHourlyUSD
			row.Monthly = fixedEIPHourlyUSD * monthlyHours
			row.Available = true
			row.Note = "Elastic IP"

		case "aws_s3_bucket":
			row.Unit = "GB-Mo"
			row.Quantity = defaultS3StorageG

			if price, err := client.getS3Price(); err == nil {
				row.Monthly = price * defaultS3StorageG
				row.Hourly = row.Monthly / monthlyHours
				row.Available = true
				row.Note = "S3 Standard (assumes 1 GB)"
			} else {
				row.Note = fmt.Sprintf("S3 pricing error: %s", err.Error())
				addWarning(row.Note)
			}

		case "aws_lambda_function":
			memMB := getFloatValue(res.Values, "memory_size", 128)
			durationSec := 0.2
			hourlyInvocations := float64(lambdaInvocations) / 24.0

			row.Note = "Lambda (estimated)"

			// Duration cost
			if price, err := client.getLambdaDurationPrice(); err == nil {
				hourlyGBSeconds := (memMB / 1024.0) * durationSec * hourlyInvocations
				row.Hourly += hourlyGBSeconds * price
				row.Available = true
			}

			// Request cost
			if price, err := client.getLambdaRequestPrice(); err == nil {
				row.Hourly += hourlyInvocations * price
				row.Available = true
			}

			row.Monthly = row.Hourly * monthlyHours

			if !row.Available {
				row.Note = "Lambda pricing unavailable"
			}

		default:
			row.Note = "pricing not implemented"
		}

		// Update summary
		if !row.Available {
			summary.UnavailableCount++
		} else {
			summary.TotalHourly += row.Hourly
			summary.TotalMonthly += row.Monthly
		}

		summary.Resources = append(summary.Resources, row)
	}

	if len(warnings) == 0 {
		return summary, ""
	}

	sort.Strings(warnings)
	return summary, strings.Join(warnings, "\n")
}

// ─── Helper Functions ────────────────────────────────────────────────────────

func buildCacheKey(serviceCode string, filters map[string]string) string {
	var keys []string
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	parts := []string{serviceCode}
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k, filters[k]))
	}

	return strings.Join(parts, "_")
}

func normalizeRDSEngine(engine string) string {
	engine = strings.ToLower(strings.TrimSpace(engine))
	switch engine {
	case "mysql":
		return "MySQL"
	case "postgres", "postgresql":
		return "PostgreSQL"
	case "mariadb":
		return "MariaDB"
	case "aurora", "aurora-mysql":
		return "Aurora MySQL"
	case "aurora-postgresql":
		return "Aurora PostgreSQL"
	case "sqlserver-ex", "sqlserver-web", "sqlserver-se", "sqlserver-ee":
		return "SQL Server"
	case "oracle-se2", "oracle-se1", "oracle-se", "oracle-ee":
		return "Oracle"
	default:
		return engine
	}
}

func normalizeRDSStorageType(storageType string) string {
	storageType = strings.ToLower(strings.TrimSpace(storageType))
	switch storageType {
	case "gp2":
		return "General Purpose"
	case "gp3":
		return "General Purpose-GP3"
	case "io1":
		return "Provisioned IOPS"
	case "io2":
		return "Provisioned IOPS-IO2"
	case "magnetic", "standard":
		return "Magnetic"
	default:
		return storageType
	}
}

func normalizeEBSVolumeType(volumeType string) string {
	volumeType = strings.ToLower(strings.TrimSpace(volumeType))
	switch volumeType {
	case "gp2":
		return "General Purpose"
	case "gp3":
		return "General Purpose-GP3"
	case "io1":
		return "Provisioned IOPS"
	case "io2":
		return "Provisioned IOPS-IO2"
	case "st1":
		return "Throughput Optimized HDD"
	case "sc1":
		return "Cold HDD"
	case "standard":
		return "Magnetic"
	default:
		return volumeType
	}
}

func getStringValue(values map[string]interface{}, key, defaultValue string) string {
	if v, ok := values[key]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return defaultValue
}

func getFloatValue(values map[string]interface{}, key string, defaultValue float64) float64 {
	if v, ok := values[key]; ok {
		switch val := v.(type) {
		case float64:
			return val
		case int:
			return float64(val)
		case string:
			if f, err := strconv.ParseFloat(val, 64); err == nil {
				return f
			}
		}
	}
	return defaultValue
}

func getBoolValue(values map[string]interface{}, key string, defaultValue bool) bool {
	if v, ok := values[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
	}
	return defaultValue
}

// ─── ASG Launch Template Resolution ──────────────────────────────────────────

func launchTemplateInstanceTypes(plan *TFPlan) map[string]string {
	result := make(map[string]string)

	for _, res := range plan.PlannedValues.RootModule.Resources {
		if res.Type == "aws_launch_template" {
			if instanceType := getStringValue(res.Values, "instance_type", ""); instanceType != "" {
				result[res.Address] = instanceType
			}
		}
	}

	return result
}

func resolveASGInstanceType(plan *TFPlan, asgAddress string, asgValues map[string]interface{}, ltMap map[string]string) string {
	// At plan time, the launch_template's name/id is computed-after-apply, so
	// planned_values shows only {"version": "$Latest"}. The reference lives in
	// configuration.expressions instead.
	if ltAddr := referencedLaunchTemplate(plan, asgAddress); ltAddr != "" {
		if it, ok := ltMap[ltAddr]; ok {
			return it
		}
	}

	// Try mixed_instances_policy first
	if mixed, ok := asgValues["mixed_instances_policy"].([]interface{}); ok && len(mixed) > 0 {
		if policy, ok := mixed[0].(map[string]interface{}); ok {
			if lt, ok := policy["launch_template"].([]interface{}); ok && len(lt) > 0 {
				if ltSpec, ok := lt[0].(map[string]interface{}); ok {
					if override, ok := ltSpec["override"].([]interface{}); ok && len(override) > 0 {
						if o, ok := override[0].(map[string]interface{}); ok {
							if it := getStringValue(o, "instance_type", ""); it != "" {
								return it
							}
						}
					}
					if spec, ok := ltSpec["launch_template_specification"].([]interface{}); ok && len(spec) > 0 {
						if s, ok := spec[0].(map[string]interface{}); ok {
							if name := getStringValue(s, "name", ""); name != "" {
								for addr, it := range ltMap {
									if strings.Contains(addr, name) {
										return it
									}
								}
							}
						}
					}
				}
			}
		}
	}

	// Try launch_template
	if lt, ok := asgValues["launch_template"].([]interface{}); ok && len(lt) > 0 {
		if ltSpec, ok := lt[0].(map[string]interface{}); ok {
			if name := getStringValue(ltSpec, "name", ""); name != "" {
				for addr, instanceType := range ltMap {
					if strings.Contains(addr, name) {
						return instanceType
					}
				}
			}
		}
	}

	return "m5.large"
}

// referencedLaunchTemplate walks the configuration block of the given ASG and
// returns the terraform address (e.g. "aws_launch_template.frontend_lt") of
// the first launch template it references — across launch_template and
// mixed_instances_policy paths. Returns "" if no reference is found.
func referencedLaunchTemplate(plan *TFPlan, asgAddress string) string {
	for _, cfg := range plan.Configuration.RootModule.Resources {
		if cfg.Address == asgAddress {
			return findLaunchTemplateRef(cfg.Expressions)
		}
	}
	return ""
}

// findLaunchTemplateRef recursively walks a parsed JSON value looking for a
// "references" list that points at an aws_launch_template resource.
func findLaunchTemplateRef(v interface{}) string {
	switch val := v.(type) {
	case map[string]interface{}:
		if refs, ok := val["references"].([]interface{}); ok {
			for _, r := range refs {
				s, ok := r.(string)
				if !ok || !strings.HasPrefix(s, "aws_launch_template.") {
					continue
				}
				// Strip trailing attribute access like ".id" / ".arn".
				parts := strings.SplitN(s, ".", 3)
				if len(parts) >= 2 {
					return parts[0] + "." + parts[1]
				}
			}
		}
		for _, child := range val {
			if r := findLaunchTemplateRef(child); r != "" {
				return r
			}
		}
	case []interface{}:
		for _, child := range val {
			if r := findLaunchTemplateRef(child); r != "" {
				return r
			}
		}
	}
	return ""
}
