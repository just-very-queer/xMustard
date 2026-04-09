# Planning: xMustard Enhancement Roadmap

## Overview

This document outlines the implementation plan for enhancing Co_Titan_Bug_Tracker into **Project xMustard**, incorporating features inspired by 11 leading AI coding agent projects.

## Phase 1: Foundation (Alpha)

### 1.1 Project Rename
- [x] Rename project to "xMustard" in all files
- [x] Update README.md
- [x] Update ARCHITECTURE.md
- [ ] Update package.json, pyproject.toml
- [ ] Update all internal references

### 1.2 Branch Creation
- [x] Created branch: `alpha.test.001.ideation.architecture.o1`

## Phase 2: Planning Checkpoint System

Inspired by SWE-agent, AutoCodeRover

### 2.1 Plan Generation
- [ ] Add `planning` field to RunRecord model
- [ ] Create `/api/runs/{run_id}/plan` endpoint
- [ ] Implement planning prompt template
- [ ] Store plan in run record for UI display

### 2.2 Plan Approval UI
- [ ] Add plan preview panel in ExecutionPane
- [ ] Implement Approve/Modify/Cancel buttons
- [ ] Add plan modification textarea
- [ ] Connect to backend plan approval endpoint

### 2.3 Plan-Aware Execution
- [ ] Modify runtimes.py to wait for plan approval
- [ ] Add `wait_for_approval` flag to RunRequest
- [ ] Implement execution resume after approval

**Backend files to modify:**
- `backend/app/models.py` - Add PlanPhase enum, planning fields
- `backend/app/service.py` - Add plan generation, approval logic
- `backend/app/runtimes.py` - Add approval gate
- `backend/app/main.py` - Add plan endpoints

**Frontend files to modify:**
- `frontend/src/components/ExecutionPane.tsx` - Plan preview UI
- `frontend/src/lib/api.ts` - Plan approval API calls
- `frontend/src/lib/types.ts` - Plan types

## Phase 3: Cost Tracking

Inspired by OpenHands Metrics, SWE-agent InstanceStats

### 3.1 Metrics Model
- [ ] Add `RunMetrics` model to models.py
- [ ] Add `metrics` field to RunRecord
- [ ] Update cost_usd calculation in runtimes.py

### 3.2 Token Counting
- [ ] Add token counting to runtime calls
- [ ] Store prompt/completion token breakdown
- [ ] Calculate USD cost using model pricing

### 3.3 Cost Dashboard UI
- [ ] Create CostSummary component
- [ ] Add cost column to run list
- [ ] Display per-issue cost breakdown
- [ ] Add workspace total cost display

**Backend files to modify:**
- `backend/app/models.py` - Add RunMetrics
- `backend/app/runtimes.py` - Add token counting
- `backend/app/service.py` - Aggregate cost data

**Frontend files to create/modify:**
- `frontend/src/components/CostSummary.tsx` (new)
- `frontend/src/components/QueuePane.tsx` - Add cost column
- `frontend/src/App.tsx` - Add cost tab

## Phase 4: Triage Automation

Inspired by trIAge, PR-Agent

### 4.1 Issue Quality Scoring
- [ ] Add `IssueQualityScore` model
- [ ] Implement completeness checker (has repro, severity, evidence?)
- [ ] Implement clarity scorer (title, description quality)
- [ ] Calculate overall quality score on scan

### 4.2 Duplicate Detection
- [ ] Add similarity scoring between issues
- [ ] Implement fingerprinting for exact duplicates
- [ ] Add "similar issues" suggestions
- [ ] Display duplicate warnings on create

### 4.3 Auto-Triage Suggestions
- [ ] Severity auto-suggestion based on impact
- [ ] Label suggestion based on code patterns
- [ ] Assign to suggested owner based on git blame

**Backend files to modify:**
- `backend/app/models.py` - Add IssueQualityScore
- `backend/app/scanners.py` - Add quality scoring
- `backend/app/service.py` - Add triage methods

## Phase 5: Verification Enhancement

Inspired by Qodo Cover

### 5.1 Coverage Tracking
- [ ] Add coverage parsing for common formats (Cobertura, JaCoCo)
- [ ] Store baseline coverage on workspace load
- [ ] Run coverage after fix verification

### 5.2 Coverage Delta Display
- [ ] Add `CoverageResult` model
- [ ] Calculate delta in verification
- [ ] Display coverage improvement in UI

### 5.3 Test Generation Suggestions
- [ ] Add suggested tests to FixRecord
- [ ] Prompt agent to add tests in plan
- [ ] Track tests_added per issue

**Backend files to modify:**
- `backend/app/models.py` - Add CoverageResult
- `backend/app/service.py` - Add coverage methods
- `backend/app/runtimes.py` - Add coverage run step

## Phase 6: Post-Run Review Artifacts

Inspired by PR-Agent

### 6.1 Patch Critique
- [ ] Add patch_review field to FixRecord
- [ ] Generate critique after run completion
- [ ] Store critique in fix_records.json

### 6.2 Improvement Suggestions
- [ ] Analyze code for potential improvements
- [ ] Suggest refactoring opportunities
- [ ] Add suggestions to verification record

### 6.3 Review UI
- [ ] Add critique panel to FixRecord display
- [ ] Show improvement suggestions
- [ ] Allow dismiss/accept per suggestion

## Phase 7: External Integrations

Inspired by OpenHands, PR-Agent

### 7.1 GitHub Integration
- [ ] Add GitHub provider abstraction
- [ ] Import issues from GitHub
- [ ] Create PR from accepted fix

### 7.2 Slack Integration
- [ ] Add Slack webhook support
- [ ] Notify on run completion
- [ ] Notify on verification success/failure

### 7.3 Linear/Jira Integration
- [ ] Sync issues bidirectionally
- [ ] Map status between systems
- [ ] Preserve labels and metadata

## Implementation Priority

| Priority | Feature | Phase | Effort |
|----------|---------|-------|--------|
| 1 | Planning checkpoint system | Phase 2 | Medium |
| 2 | Cost tracking | Phase 3 | Low |
| 3 | Issue quality scoring | Phase 4 | Medium |
| 4 | Coverage tracking | Phase 5 | Medium |
| 5 | Post-run review | Phase 6 | Low |
| 6 | GitHub integration | Phase 7 | Medium |

## File Change Summary

### Backend Changes

| File | Changes |
|------|---------|
| `app/models.py` | +RunMetrics, +IssueQualityScore, +CoverageResult, +PlanPhase |
| `app/service.py` | +plan_generation(), +cost_aggregation(), +triage_methods() |
| `app/runtimes.py` | +approval_gate(), +token_counting(), +coverage_step() |
| `app/main.py` | +POST /runs/{id}/plan, +POST /runs/{id}/approve_plan |
| `app/cli.py` | +plan command, +cost command |

### Frontend Changes

| File | Changes |
|------|---------|
| `App.tsx` | +CostDashboard tab, +PlanApproval modal |
| `ExecutionPane.tsx` | +Plan preview, +Approve/Modify buttons |
| `CostSummary.tsx` | New component |
| `QueuePane.tsx` | +Cost column, +Quality indicators |
| `types.ts` | +RunMetrics, +PlanPhase, +CoverageResult |
| `api.ts` | +plan approval API, +cost API |

## Testing

- [ ] Unit tests for new models
- [ ] Integration tests for plan approval flow
- [ ] Integration tests for cost calculation
- [ ] UI tests for new components

## Documentation

- [x] README.md updated
- [x] ARCHITECTURE.md updated
- [ ] PLANNING.md (this file)
- [ ] FEATURES.md (detailed feature specs)
- [ ] CHANGELOG.md (version history)
