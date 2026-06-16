# xMustard Repository Guide

This repository contains xMustard, a local bug-operations system for software repositories. The backend is Go (`api-go`, an HTTP shell on :8042) calling a Rust core (`rust-core`), with a React TypeScript frontend in `frontend/`. The original Python FastAPI backend was retired to `archive/2026-06-16-python-backend/` (see `docs/PYTHON_TO_RUST_MIGRATION.md`).

## What This Repo Is For

- load a repository into a workspace snapshot
- track issues, signals, runs, fixes, and verification evidence
- launch local coding-agent runs against issues
- review plans, costs, critique, improvements, and run insights

## Working Style

- keep the product issue-first and evidence-first
- prefer repo guidance and durable artifacts over chat-only behavior
- avoid adding noisy heuristic scanning when guidance or verification can solve the same problem better
- preserve user changes and avoid destructive git actions unless explicitly requested

## Repository Structure

- `api-go/`: Go HTTP backend (routes, request shaping, persistence) + `xmustard-ops` CLI
- `rust-core/`: Rust core — scanner, repo map, verification, diagnostics, lsp, goal/swarm runtime, data models, semantic search
- `backend/`: runtime `data/` + `sql/` only (Python retired to `archive/`)
- `frontend/src/`: React app, queue views, detail panes, and shared client types
- `docs/`: planning, architecture, features, changelog, and research synthesis
- `research/`: cloned reference repos used to shape the product roadmap

## Preferred Checks

Backend work:

- `cd api-go && go test ./...`
- `cd api-go && go build ./...`
- `cd rust-core && cargo test && cargo clippy`

Frontend work:

- `cd frontend && npm run lint`
- `cd frontend && npm run build`

## Guidance For Agents

- keep backend and frontend contracts in sync
- surface backend capabilities in the UI before inventing new ones
- favor concise, inspectable markdown guidance files
- when updating docs, keep them aligned with the code that actually ships
