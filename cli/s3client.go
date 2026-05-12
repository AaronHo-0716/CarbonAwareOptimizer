package main

import (
	"bufio"
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	rgtypes "github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/smithy-go"
	"github.com/oklog/ulid/v2"
)

// carbonOptimizerTag is applied to every bucket we create and matched on lookup.
const carbonOptimizerTag = "Carbon-Optimizer"

// s3Client bundles the SDK clients we use plus the resolved AWS account/region
// so callers don't have to re-derive them.
type s3Client struct {
	s3        *s3.Client
	tagging   *resourcegroupstaggingapi.Client
	sts       *sts.Client
	region    string
	accountID string
}

// newS3Client builds the bundle from the standard AWS credential chain.
func newS3Client(ctx context.Context) (*s3Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	c := &s3Client{
		s3:      s3.NewFromConfig(cfg),
		tagging: resourcegroupstaggingapi.NewFromConfig(cfg),
		sts:     sts.NewFromConfig(cfg),
		region:  cfg.Region,
	}
	out, err := c.sts.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return nil, fmt.Errorf("get caller identity: %w", err)
	}
	if out.Account != nil {
		c.accountID = *out.Account
	}
	return c, nil
}

// defaultBucketName returns the deterministic name we offer in the create prompt.
func (c *s3Client) defaultBucketName() string {
	if c.accountID == "" {
		return "carbon-optimizer-" + c.region
	}
	return fmt.Sprintf("carbon-optimizer-%s-%s", c.accountID, c.region)
}

// detectBucket returns the first bucket tagged Carbon-Optimizer, or "" if none.
func (c *s3Client) detectBucket(ctx context.Context) (string, error) {
	var token *string
	for {
		out, err := c.tagging.GetResources(ctx, &resourcegroupstaggingapi.GetResourcesInput{
			ResourceTypeFilters: []string{"s3:bucket"},
			TagFilters: []rgtypes.TagFilter{
				{Key: aws.String(carbonOptimizerTag)},
			},
			PaginationToken: token,
		})
		if err != nil {
			return "", fmt.Errorf("tag lookup: %w", err)
		}
		for _, r := range out.ResourceTagMappingList {
			if r.ResourceARN == nil {
				continue
			}
			// ARN format: arn:aws:s3:::bucket-name
			parts := strings.SplitN(*r.ResourceARN, ":::", 2)
			if len(parts) == 2 && parts[1] != "" {
				return parts[1], nil
			}
		}
		if out.PaginationToken == nil || *out.PaginationToken == "" {
			return "", nil
		}
		token = out.PaginationToken
	}
}

// createBucket creates a bucket in the client's region, tags it, and locks down
// public access. Returns the bucket name on success.
func (c *s3Client) createBucket(ctx context.Context, name string) error {
	input := &s3.CreateBucketInput{Bucket: aws.String(name)}
	if c.region != "us-east-1" {
		input.CreateBucketConfiguration = &s3types.CreateBucketConfiguration{
			LocationConstraint: s3types.BucketLocationConstraint(c.region),
		}
	}
	if _, err := c.s3.CreateBucket(ctx, input); err != nil {
		var alreadyOwned *s3types.BucketAlreadyOwnedByYou
		if !errors.As(err, &alreadyOwned) {
			return fmt.Errorf("create bucket %s: %w", name, err)
		}
	}
	if _, err := c.s3.PutBucketTagging(ctx, &s3.PutBucketTaggingInput{
		Bucket: aws.String(name),
		Tagging: &s3types.Tagging{TagSet: []s3types.Tag{
			{Key: aws.String(carbonOptimizerTag), Value: aws.String("true")},
		}},
	}); err != nil {
		return fmt.Errorf("tag bucket: %w", err)
	}
	if _, err := c.s3.PutPublicAccessBlock(ctx, &s3.PutPublicAccessBlockInput{
		Bucket: aws.String(name),
		PublicAccessBlockConfiguration: &s3types.PublicAccessBlockConfiguration{
			BlockPublicAcls:       aws.Bool(true),
			IgnorePublicAcls:      aws.Bool(true),
			BlockPublicPolicy:     aws.Bool(true),
			RestrictPublicBuckets: aws.Bool(true),
		},
	}); err != nil {
		return fmt.Errorf("lock down public access: %w", err)
	}
	return nil
}

// listPlans reads _meta/runs.ndjson and returns the most-recent entry per
// plan-id (so the UI shows each plan exactly once).
func (c *s3Client) listPlans(ctx context.Context, bucket string) ([]PlanSummary, error) {
	body, err := c.getObject(ctx, bucket, "_meta/runs.ndjson")
	if err != nil {
		if isNoSuchKey(err) {
			return nil, nil
		}
		return nil, err
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	latest := make(map[string]PlanSummary)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e RunLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		prev, ok := latest[e.PlanID]
		if !ok || e.Timestamp.After(prev.Timestamp) {
			latest[e.PlanID] = PlanSummary{
				PlanID:    e.PlanID,
				RunID:     e.RunID,
				Region:    e.Region,
				Timestamp: e.Timestamp,
				Status:    e.Status,
			}
		}
	}
	out := make([]PlanSummary, 0, len(latest))
	for _, v := range latest {
		out = append(out, v)
	}
	// Newest first.
	sortPlanSummaries(out)
	return out, nil
}

func sortPlanSummaries(s []PlanSummary) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Timestamp.After(s[j-1].Timestamp); j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// reportBundle is the in-memory shape of a reports/<plan-id>/ directory.
type reportBundle struct {
	Manifest     ReportManifest
	Impacts      []ResourceImpact
	Matrix       []MatrixRow
	CostBaseline CostSummary
	CostTarget   CostSummary
	OpsSeries    OpsSeriesBundle
	AIMd         string
	PlanJSON     []byte // raw plan bytes (redacted on the way in), may be empty
}

// getReport downloads every artifact referenced by manifest.json for one plan.
func (c *s3Client) getReport(ctx context.Context, bucket, planID string) (reportBundle, error) {
	var rb reportBundle
	manifestKey := fmt.Sprintf("reports/%s/manifest.json", planID)
	mb, err := c.getObject(ctx, bucket, manifestKey)
	if err != nil {
		return rb, fmt.Errorf("manifest: %w", err)
	}
	if err := json.Unmarshal(mb, &rb.Manifest); err != nil {
		return rb, fmt.Errorf("decode manifest: %w", err)
	}
	prefix := fmt.Sprintf("reports/%s/", planID)
	get := func(rel string, out any) error {
		if rel == "" {
			return nil
		}
		body, err := c.getObject(ctx, bucket, prefix+rel)
		if err != nil {
			return err
		}
		return json.Unmarshal(body, out)
	}
	if err := get(rb.Manifest.Artifacts.Impacts, &rb.Impacts); err != nil {
		return rb, fmt.Errorf("impacts: %w", err)
	}
	if err := get(rb.Manifest.Artifacts.Matrix, &rb.Matrix); err != nil {
		return rb, fmt.Errorf("matrix: %w", err)
	}
	if err := get(rb.Manifest.Artifacts.CostBaseline, &rb.CostBaseline); err != nil {
		return rb, fmt.Errorf("cost baseline: %w", err)
	}
	if rb.Manifest.Artifacts.CostTarget != "" {
		if err := get(rb.Manifest.Artifacts.CostTarget, &rb.CostTarget); err != nil {
			return rb, fmt.Errorf("cost target: %w", err)
		}
	}
	if err := get(rb.Manifest.Artifacts.OpsSeries, &rb.OpsSeries); err != nil {
		return rb, fmt.Errorf("ops series: %w", err)
	}
	if rel := rb.Manifest.Artifacts.AIMd; rel != "" {
		if body, err := c.getObject(ctx, bucket, prefix+rel); err == nil {
			rb.AIMd = string(body)
		}
	}
	if planBytes, err := c.getObject(ctx, bucket, fmt.Sprintf("plans/%s.json", planID)); err == nil {
		rb.PlanJSON = planBytes
	}
	return rb, nil
}

// putReportBundle uploads every artifact for a single run with last-write-wins
// semantics (no per-run subfolder).
func (c *s3Client) putReportBundle(ctx context.Context, bucket, planID string, b reportBundle) error {
	prefix := fmt.Sprintf("reports/%s/", planID)
	put := func(rel, contentType string, body []byte) error {
		_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
			Bucket:      aws.String(bucket),
			Key:         aws.String(prefix + rel),
			Body:        bytes.NewReader(body),
			ContentType: aws.String(contentType),
		})
		return err
	}
	putJSON := func(rel string, v any) error {
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return err
		}
		return put(rel, "application/json", data)
	}
	if err := putJSON("impacts.json", b.Impacts); err != nil {
		return fmt.Errorf("impacts: %w", err)
	}
	if err := putJSON("matrix.json", b.Matrix); err != nil {
		return fmt.Errorf("matrix: %w", err)
	}
	if err := putJSON("cost-baseline.json", b.CostBaseline); err != nil {
		return fmt.Errorf("cost-baseline: %w", err)
	}
	if b.Manifest.Artifacts.CostTarget != "" {
		if err := putJSON("cost-target.json", b.CostTarget); err != nil {
			return fmt.Errorf("cost-target: %w", err)
		}
	}
	if err := putJSON("ops-series.json", b.OpsSeries); err != nil {
		return fmt.Errorf("ops-series: %w", err)
	}
	if b.AIMd != "" {
		if err := put("ai-recommendation.md", "text/markdown; charset=utf-8", []byte(b.AIMd)); err != nil {
			return fmt.Errorf("ai md: %w", err)
		}
	}
	if err := putJSON("manifest.json", b.Manifest); err != nil {
		return fmt.Errorf("manifest: %w", err)
	}
	return nil
}

// putPDF uploads the PDF bytes as reports/<plan-id>/report.pdf.
func (c *s3Client) putPDF(ctx context.Context, bucket, planID string, pdfBytes []byte) error {
	if len(pdfBytes) == 0 {
		return nil
	}
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(fmt.Sprintf("reports/%s/report.pdf", planID)),
		Body:        bytes.NewReader(pdfBytes),
		ContentType: aws.String("application/pdf"),
	})
	return err
}

// putPlanJSON uploads (already-redacted) plan bytes to plans/<plan-id>.json.
func (c *s3Client) putPlanJSON(ctx context.Context, bucket, planID string, body []byte) error {
	_, err := c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(fmt.Sprintf("plans/%s.json", planID)),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
	})
	return err
}

// appendRunLog downloads _meta/runs.ndjson (if any), appends one line, and
// re-puts the whole thing. Last-write-wins; not safe under concurrent uploads.
func (c *s3Client) appendRunLog(ctx context.Context, bucket string, entry RunLogEntry) error {
	const key = "_meta/runs.ndjson"
	body, err := c.getObject(ctx, bucket, key)
	if err != nil && !isNoSuchKey(err) {
		return fmt.Errorf("read run log: %w", err)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	if len(body) > 0 {
		buf.Write(body)
		if !bytes.HasSuffix(body, []byte("\n")) {
			buf.WriteByte('\n')
		}
	}
	buf.Write(line)
	buf.WriteByte('\n')
	_, err = c.s3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(bucket),
		Key:         aws.String(key),
		Body:        bytes.NewReader(buf.Bytes()),
		ContentType: aws.String("application/x-ndjson"),
	})
	return err
}

func (c *s3Client) getObject(ctx context.Context, bucket, key string) ([]byte, error) {
	out, err := c.s3.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, err
	}
	defer out.Body.Close()
	return io.ReadAll(out.Body)
}

func isNoSuchKey(err error) bool {
	if err == nil {
		return false
	}
	var nsk *s3types.NoSuchKey
	if errors.As(err, &nsk) {
		return true
	}
	var ae smithy.APIError
	if errors.As(err, &ae) {
		switch ae.ErrorCode() {
		case "NoSuchKey", "NotFound":
			return true
		}
	}
	return false
}

// newRunID generates a ULID for use as a run identifier.
func newRunID(now time.Time) string {
	return ulid.MustNew(ulid.Timestamp(now), cryptorand.Reader).String()
}
