# S3 + Terraform JSON Upload — Data-Model Draft

**Status:** draft, not implemented. Solicits review on layout, naming, and auth before any Go code lands.

## Goal

Let a user upload their Terraform plan JSON to **their own S3 bucket**, run the Carbon-Aware Optimizer against that plan (interactively or headless), and persist analysis results back to the same bucket so they can browse, share, or feed them into other tooling.

Constraints:

- The user owns the bucket. We never touch credentials outside the standard AWS chain.
- Plan JSON can be large; results are a few hundred KB max.
- A given plan may be re-analyzed many times as the user iterates on simulations.

## Bucket layout

```
s3://<user-bucket>/<prefix>/
├── plans/                       # uploaded Terraform plan JSON
│   └── <plan-id>.json
├── reports/                     # analysis results, keyed by plan-id
│   └── <plan-id>/
│       ├── manifest.json        # index of artifacts + run metadata
│       ├── impacts.json         # []ResourceImpact
│       ├── matrix.json          # []MatrixRow
│       ├── cost-baseline.json   # CostSummary
│       ├── cost-target.json     # CostSummary (omitted when no sim applied)
│       ├── ops-series.json      # { "baseline": []OpsEmissionPoint, "sim": []OpsEmissionPoint }
│       ├── ai-recommendation.md # raw markdown source (unrendered)
│       └── report.pdf           # same PDF the TUI produces
└── _meta/
    └── runs.ndjson              # append-only run log
```

`<prefix>` is optional and defaults to empty. `<plan-id>` is the join key between a plan and its report(s).

## `manifest.json` schema

```json
{
  "run_id":      "01HZXABCDEFGHJKMNPQRSTVWXY",
  "plan_id":     "user-prod-2026Q2",
  "plan_uri":    "s3://my-co2-bucket/plans/user-prod-2026Q2.json",
  "region":      "us-east-1",
  "generated_at":"2026-05-12T14:33:00Z",
  "tool_version":"carbon-optimizer 0.4.0",
  "simulation": {
    "applied":       true,
    "optimization":  "graviton",
    "target_region": "us-east-1"
  },
  "artifacts": {
    "impacts":       "impacts.json",
    "matrix":        "matrix.json",
    "cost_baseline": "cost-baseline.json",
    "cost_target":   "cost-target.json",
    "ops_series":    "ops-series.json",
    "ai_md":         "ai-recommendation.md",
    "pdf":           "report.pdf"
  },
  "totals": {
    "ops_kg_day":   12.34,
    "emb_kg_day":    3.21,
    "monthly_usd": 482.10
  }
}
```

`artifacts` paths are relative to the manifest's own object (i.e. `reports/<plan-id>/`).

## `runs.ndjson`

One JSON object per line, append-only:

```json
{"run_id":"01HZX...","plan_id":"user-prod-2026Q2","region":"us-east-1","timestamp":"2026-05-12T14:33:00Z","status":"ok"}
```

Used for `carbon-optimizer ls` to show recent activity without listing the whole bucket.

## Auth & credentials

- Standard AWS credential chain: `AWS_PROFILE`, env vars, IRSA, IMDS. The CLI is a thin S3 client; no keys ever leave the user's machine.
- Bucket + prefix configured via one of:
  - flags: `--s3-bucket=<name> --s3-prefix=<root>`
  - env: `CARBON_S3_BUCKET`, `CARBON_S3_PREFIX`
  - persistent config at `~/.carbon-optimizer/config.toml`

Required IAM permissions on the bucket:

```
s3:GetObject       on  <bucket>/plans/*           (read plans)
s3:PutObject       on  <bucket>/reports/*
                       <bucket>/_meta/runs.ndjson (write results + log)
s3:ListBucket      on  <bucket>                   (for `ls`)
```

## CLI surface (drafted)

| Command | Purpose |
| --- | --- |
| `carbon-optimizer push <local.json> --plan-id <id>` | Upload only |
| `carbon-optimizer pull s3://<b>/plans/<id>.json` | Fetch + open TUI; on Apply, upload bundle back |
| `carbon-optimizer run --plan-id <id> --bucket <b> [--headless]` | Analyze; upload bundle. With `--headless`, no TUI |
| `carbon-optimizer ls --bucket <b>` | Tail `_meta/runs.ndjson` |
| `carbon-optimizer fetch --plan-id <id> --bucket <b>` | Download report bundle |

## Go types (sketched; live in `cli/s3types.go` when implemented)

```go
type ReportManifest struct {
    RunID       string         `json:"run_id"`
    PlanID      string         `json:"plan_id"`
    PlanURI     string         `json:"plan_uri"`
    Region      string         `json:"region"`
    GeneratedAt time.Time      `json:"generated_at"`
    ToolVersion string         `json:"tool_version"`
    Simulation  SimulationMeta `json:"simulation"`
    Artifacts   ArtifactPaths  `json:"artifacts"`
    Totals      ReportTotals   `json:"totals"`
}

type SimulationMeta struct {
    Applied      bool   `json:"applied"`
    Optimization string `json:"optimization"`   // "graviton" | "region" | ""
    TargetRegion string `json:"target_region"`
}

type ArtifactPaths struct {
    Impacts      string `json:"impacts"`
    Matrix       string `json:"matrix"`
    CostBaseline string `json:"cost_baseline"`
    CostTarget   string `json:"cost_target,omitempty"`
    OpsSeries    string `json:"ops_series"`
    AIMd         string `json:"ai_md,omitempty"`
    PDF          string `json:"pdf"`
}

type ReportTotals struct {
    OpsKgDay   float64 `json:"ops_kg_day"`
    EmbKgDay   float64 `json:"emb_kg_day"`
    MonthlyUSD float64 `json:"monthly_usd"`
}

type OpsSeriesBundle struct {
    Baseline  []OpsEmissionPoint `json:"baseline"`
    Simulated []OpsEmissionPoint `json:"sim,omitempty"`
}

type RunLogEntry struct {
    RunID     string    `json:"run_id"`
    PlanID    string    `json:"plan_id"`
    Region    string    `json:"region"`
    Timestamp time.Time `json:"timestamp"`
    Status    string    `json:"status"` // "ok" | "error"
    Error     string    `json:"error,omitempty"`
}
```

All other artifact files reuse existing types directly serialized as JSON:

- `impacts.json`     ← `[]ResourceImpact`
- `matrix.json`      ← `[]MatrixRow`
- `cost-*.json`      ← `CostSummary`
- `ai-recommendation.md` ← raw markdown string (no envelope)
- `report.pdf`       ← bytes from `buildReportPDF`

## Open questions

These are intentionally unresolved; resolve before implementation:

1. **Overwrite vs. append for re-runs.** Replace `reports/<plan-id>/` on each run, or write to `reports/<plan-id>/<run-id>/` and keep the latest behind a `current` pointer? Append is safer but ~2× storage.
2. **Plan ID source.** Content hash (great for dedup, opaque to humans), user-supplied slug (human-friendly, collision-prone), or ULID (unique but loses correlation to a Terraform repo)? Likely "user slug or fallback to content hash".
3. **Concurrent uploads.** Locking via `_meta/locks/<plan-id>.lock` (DynamoDB is cleaner if we want CAS), or accept last-write-wins for a single-user workflow?
4. **Bucket region.** Should `manifest.json` record both the analyzed AWS region *and* the bucket's region? Useful if a user keeps cross-region buckets.
5. **Secrets in plans.** Terraform plan JSON often contains secret values in `variables[].value`. Should we redact known-sensitive keys (`*password*`, `*token*`, `*key*`) before upload? Or warn loudly and let the user opt in?
6. **Retention.** Do we offer an automatic prune (e.g. `--max-runs 10`), or rely on bucket-level lifecycle rules?

## Out of scope (this draft)

- Cross-account access (write to a shared service bucket).
- Web UI on top of the bucket.
- KMS/CMK key configuration.
- Versioning beyond what S3 itself provides.
