# Carbon-Aware Optimizer — Technical Overview

A terminal-UI (TUI) application written in **Go 1.26.2** that ingests a Terraform plan describing AWS infrastructure, computes per-resource carbon and cost impact, simulates optimisations (ARM/Graviton, region move), and persists the resulting report to S3 and/or PDF.

The entire application is a single Go binary (`carbon-optimizer`) built from the `cli/` package (~6.9k LOC, `package main`).

---

## 1. Tech stack

| Layer | Library |
|---|---|
| TUI runtime | `github.com/charmbracelet/bubbletea` (Elm-style Model / Update / View) |
| Widgets | `charmbracelet/bubbles` — `textinput`, `spinner`, `viewport`, `table` |
| Styling | `charmbracelet/lipgloss` (+ `lipgloss/v2` for charts) |
| Markdown rendering | `charmbracelet/glamour` (ANSI render of AI output) |
| Charts | `NimbleMarkets/ntcharts/v2` — `linechart` and `timeserieslinechart` |
| PDF | `jung-kurt/gofpdf` |
| AWS | `aws-sdk-go-v2` — `s3`, `sts`, `resourcegroupstaggingapi` |
| IDs | `oklog/ulid/v2` for run IDs |
| External APIs | ElectricityMaps (grid intensity), OpenRouter (AI suggestions, `openai/gpt-4o-mini`) |

Terraform CLI is invoked via `os/exec` when the user picks a `.tf` file or a directory (auto-runs `terraform init`/`plan`/`show -json`). When the user picks a `plan.json` directly, the binary stays self-contained.

---

## 2. Source layout (`cli/`)

| File | Purpose |
|---|---|
| `main.go` | Boots `tea.NewProgram` with `WithAltScreen` + `WithMouseCellMotion`. |
| `tui.go` | Bubble Tea `model`, `Init`, `Update`, top-level key routing, async `tea.Cmd`s (`runAnalysisCmd`, `runReanalysisCmd`, `runRegionSimCmd`, `fetchAICmd`). |
| `view.go` | All Lipgloss rendering: title bar, main viewport, side panel, regional matrix table, charts. |
| `types.go` | Domain types (`TFPlan`, `ResourceImpact`, `CostSummary`, …), `appState` enum, side-panel `sideItem` enum, all Tea message types. |
| `carbon.go` | Plan loader (file or `terraform show -json`), CSV coefficient loaders, the core `calculateImpact` / `calculateImpactWithSeries` engine, Graviton variant mapping. |
| `pricing.go` | AWS pricing lookup against the public Pricing API, with file-system caching under `.cache/aws-pricing/`. |
| `api.go` | ElectricityMaps clients (latest + past-range series), region peer-group matrix construction, OpenRouter call. |
| `filepicker.go` | Custom file-picker widget (built from scratch — only shows `.json` / `.tf` / directories). |
| `export_pdf.go` (+ `_test.go`) | A4 PDF builder; embeds the impact table, regional matrix, side-by-side simulation table, and AI markdown rendered to PDF text. |
| `s3client.go` | AWS SDK wrapper: tag-based bucket discovery, create-with-PAB-lockdown, report bundle put/get, run-log append, delete cascade. |
| `s3types.go` | On-disk JSON schemas: `ReportManifest`, `RunLogEntry`, `OpsSeriesBundle`, `PlanSummary`. |
| `s3source.go` | "Load prior run" TUI state machine (`stateS3Source`, `stateS3DeleteConfirm`). |
| `s3upload.go` | Upload flow — slug prompt, optional bucket-create, command snapshotting to avoid torn reads of the live model. |
| `s3load.go` | Inflate a downloaded `reportBundle` back into the live `model`. |
| `s3redact.go` (+ `_test.go`) | Defence-in-depth redaction of plan JSON before upload (sensitive-flag tree walk + `password/secret/token/api-key/private-key` regex). |

---

## 3. TUI state machine

`appState` (in `types.go`) drives `View()` dispatch in `view.go`:

```
stateAPIKeys          → token popup (ElectricityMaps required, OpenRouter optional)
stateS3Probing        → spinner while STS+tag lookup runs
stateS3Source         → list of prior runs from runs.ndjson, or "browse local"
stateS3CreateBucket   → bucket-name confirmation overlay
stateS3SlugInput      → plan-id slug entry before upload (must match [a-z0-9-]{1,63})
stateS3DeleteConfirm  → y/n overlay
stateFilePicker       → custom file picker, .json or .tf only
stateLoading          → spinner while analysis is in flight
stateResults          → split-pane: main viewport (left) + settings/optimisation side panel (right)
```

The results view has two focus targets (`focusMain` / `focusSide`) toggled with **Tab**. The side panel itself is a dynamic list (`activeSideItems()`) that filters items based on whether the plan contains network or Lambda resources, and whether the optimisation type is `"region"` (revealing a region text input).

Key global shortcuts in `stateResults`:
- `Ctrl+P` — export PDF
- `Ctrl+S` — upload to S3
- `f` — return to file picker
- `q` / `Ctrl+C` — quit

Async work is dispatched through `tea.Cmd` closures that return one of the typed messages (`analysisCompleteMsg`, `aiCompleteMsg`, `simCompleteMsg`, `s3*Msg`, `pdfExportMsg`, `errMsg`). The model is otherwise immutable per `Update` call (classic Bubble Tea pattern).

---

## 4. Carbon model

Implemented in `carbon.go`. Constants:

```go
const PUE = 1.135          // power-usage effectiveness
const LifespanDays = 1460  // amortise embodied carbon over 4 years
```

### Inputs
- **Embodied coefficients** — `coefficients-aws-embodied.csv` (kg CO₂e per instance, column 7).
- **Use coefficients** — `coefficients-aws-use.csv`; averaged into `x86Coeff` and `armCoeff` UseCoeff structs (`MinWatts`, `MaxWatts`). Falls back to `{0.8, 4.0}` x86 / `{0.47, 1.7}` ARM.
- **vCPU map** — `aws-instances-latest-2026.csv`, with heuristic fallbacks based on size suffix.
- **Grid intensity** — ElectricityMaps `/v3/carbon-intensity/past-range` over a rolling 24h window; cached in-process per region in `model.intensityCache`. Falls back to 250 gCO₂e/kWh.

### Per-resource formulas

Compute (`aws_instance`, `aws_autoscaling_group`, `aws_db_instance`):
```
avgWatts   = min + (max - min) * utilization
dailyKWh   = (avgWatts * vCPUs * 24 * PUE) / 1000
dailyOps   = dailyKWh * gridIntensity / 1000 * count   // kg CO₂/day
dailyEmb   = embodied[type] / LifespanDays * count
```

Where `utilization` comes from a `UtilizationSchedule { WorkStartHour, WorkEndHour, WorkPct, IdlePct }`. The `calculateImpactWithSeries` variant walks the hourly intensity series and applies `utilizationAtTime(schedule, ts)` per point, producing both a scalar daily total and a `[]OpsEmissionPoint` time series for the chart.

Storage (`aws_ebs_volume`): density coefficients differ for SSD (`gp2/gp3/io1/io2`) vs HDD: `wPerGB ∈ {0.0012, 0.0065}`, `embPerTB ∈ {50, 20}`.

Network (`aws_lb`/`aws_alb`/`aws_elb`, plus per-instance overhead): `trafficByProfile(profile)` returns 10 / 100 / 1000 GB/mo for low/medium/high, divided to per-day and multiplied by 0.001 kWh/GB.

Lambda: `dur = (200ms * invocations) / 3_600_000ms/h`; energy uses x86 min-watts × memory ratio.

DB storage overhead is added when `aws_db_instance.allocated_storage` is present.

### Graviton simulation
`simulateGraviton(plan)` deep-copies the plan and rewrites instance types via a static map (`m5.→m6g.`, `c5.→c7g.`, `r5.→r7g.`, `t3.→t4g.`, etc.). It runs *locally* — no API calls — because the only thing that changes is which `UseCoeff` table the engine selects (instances ending in `g` or containing `r7g` use `armCoeff`).

### Region simulation
`runRegionSimCmd` fetches the target region's intensity series (cache-first), re-runs `calculateImpactWithSeries`, re-prices via `buildCostSummary`, and returns a `simCompleteMsg`.

### Regional trade-off matrix
`buildRegionalMatrix` derives a peer group from the current region prefix (`us-/ca-/sa-/mx-` → Americas, `eu-` → Europe, `ap-` → APAC, `me-/af-/il-` → MEA), fans out concurrent intensity fetches via `sync.WaitGroup`, sorts ascending by intensity, takes the top-5 (always including current), and computes simulated emissions and cost per row. The same data feeds both the side-by-side table in the TUI and the AI prompt context string.

---

## 5. Cost model (`pricing.go`)

`buildCostSummary(plan, region, lambdaInv)` produces a `CostSummary { Resources, TotalHourly, TotalMonthly, UnavailableCount }`. Uses the AWS Pricing API (single endpoint, `ap-south-1`) with a two-tier cache:
1. In-memory `map[string]*awsPriceResponse` on the `pricingClient`.
2. File cache at `.cache/aws-pricing/<cacheKey>.json`.

`monthlyHours = 730`. A set of free or always-bundled resource types (subnets, IAM roles, route tables, security groups, etc.) is filtered through `hiddenFreeResourceTypes`. Pricing warnings (e.g. "no on-demand price found") are surfaced into `pricingWarn` and combined across baseline and simulation via `combineWarnings`.

---

## 6. S3 persistence layer

### Discovery
`detectBucket` calls `resourcegroupstaggingapi.GetResources` filtered by `s3:bucket` + tag `Carbon-Optimizer`. First match wins. The default name for a new bucket is `carbon-optimizer-<accountId>-<region>` (from `sts:GetCallerIdentity`).

### Bucket creation
`createBucket` issues `CreateBucket` (with `LocationConstraint` outside `us-east-1`), `PutBucketTagging`, and `PutPublicAccessBlock` with all four flags set to true. Errors with `BucketAlreadyOwnedByYou` are swallowed.

### On-disk layout

```
s3://<bucket>/
├── plans/<plan-id>.json              # redacted Terraform plan
├── reports/<plan-id>/
│   ├── manifest.json                 # ReportManifest (index + totals)
│   ├── impacts.json                  # []ResourceImpact
│   ├── matrix.json                   # []MatrixRow
│   ├── cost-baseline.json            # CostSummary
│   ├── cost-target.json              # CostSummary (omitted if no sim)
│   ├── ops-series.json               # { baseline, sim }
│   ├── ai-recommendation.md          # raw markdown source
│   └── report.pdf                    # same PDF the TUI generates
└── _meta/runs.ndjson                 # append-only run log
```

The plan-id slug is user-supplied (regex `^[a-z0-9][a-z0-9-]{0,62}$`). One run per plan-id (last-write-wins, **not** safe under concurrent uploads — documented limitation).

### Run log
`_meta/runs.ndjson` is **read-modify-write**: `appendRunLog` `GetObject`s the current file, appends a JSON line, and `PutObject`s the whole thing back. `listPlans` keeps only the most recent entry per `plan_id`. `deletePlan` rewrites the log to drop a plan-id (and deletes the file entirely if zero entries remain).

### Redaction
Before any plan upload, `redactPlanBytes` walks the JSON in five passes:
1. Blank every `variables[*].value`.
2. Walk `planned_values.root_module` (+ `child_modules`) and use the parallel `sensitive_values` shape to zero matching `values` entries.
3. Same for `resource_changes[*].change.before / after` (paired with `before_sensitive` / `after_sensitive`).
4. Drop `prior_state` entirely.
5. Recursive sweep that replaces any string under a key matching `(?i)(password|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)`.

Shape is preserved so the redacted JSON is still parseable by the analyser if the user later pulls the bundle.

### Run IDs
Run IDs are ULIDs (`oklog/ulid/v2`) seeded from `time.Now()` so they're lexicographically time-sortable in the log.

---

## 7. AI integration (`api.go`)

`getAISuggestions` builds a prompt that embeds:
- the highest-impact resource (`top ResourceImpact`),
- current region + grid intensity,
- the matrix context string (live regional trade-offs).

It calls `https://openrouter.ai/api/v1/chat/completions` with `model = openai/gpt-4o-mini`, 90-second timeout. If the OpenRouter key is empty (the user left it blank on the API-keys screen), a deterministic mock response is returned so the rest of the TUI still works. Output flows through `glamour.Render(raw, "dark")` for the TUI but is stored raw too (`aiContentRaw`) so it can be reused verbatim in the PDF and the S3 bundle.

---

## 8. Layout & widgets

- **Split-pane results view**: main viewport on the left (`vpWidth = width - sideW - 2`), fixed-width side panel on the right (`sideW = 36`).
- **Side panel viewport scroll** is auto-synced (`syncSideVPOffset`) whenever the focused item changes so the cursor stays visible without dropping focus.
- **Charts** are rendered with `ntcharts/v2/linechart/timeserieslinechart` — one line per series (baseline vs simulation) with X-axis = timestamp, Y-axis = ops emissions.
- **Tables** use `bubbles/table` for the regional matrix; the side-by-side simulation comparison is a hand-rolled Lipgloss grid for tighter column alignment.

---

## 9. Configuration & secrets

- `.env` in CWD: `ELECTRICITYMAPS_TOKEN=...` (required), `OPENROUTER_API_KEY=...` (optional). Loaded/written via `loadEnv`/`saveEnv`.
- AWS credentials: default SDK chain (`AWS_PROFILE`, env vars, IMDS, IRSA). If `AWS_PROFILE` is unset when shelling out to `terraform`, the binary injects `AWS_PROFILE=lifeng` as a fallback.
- No keys ever leave the user's machine except via the explicit ElectricityMaps and OpenRouter HTTPS calls.

---

## 10. Build & data files

```
go build -o carbon-optimizer ./cli
```

Data files expected in the working directory at runtime:
- `coefficients-aws-embodied.csv`
- `coefficients-aws-use.csv`
- `aws-instances-latest-2026.csv`
- `data_centers.json` (region metadata)
- `plan.json` (sample plan for testing)

Cache directories created on demand:
- `.cache/aws-pricing/` — file cache of AWS Pricing API responses.
- `cache/` — debug JSON dumps from earlier dev sessions (not load-bearing).

---

## 11. Known constraints

- Last-write-wins on `_meta/runs.ndjson` and `reports/<plan-id>/*` — single-user workflow only; concurrent uploads can lose data.
- Cross-account S3 access not supported; bucket is auto-discovered in the caller's account/region.
- KMS/CMK encryption beyond S3 defaults not configured.
- ElectricityMaps fallback intensity (250 gCO₂e/kWh) is used when the API is unreachable, which silently degrades the carbon math — surfaced only via the absence of a populated time series in the chart.
