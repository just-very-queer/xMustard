# xMustard Repository Guide

This repository contains xMustard, a local bug-operations system for software repositories. It has a Python FastAPI backend in `backend/` and a React TypeScript frontend in `frontend/`.

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

- `backend/app/`: API, models, orchestration, scanners, runtimes, store, and CLI
- `backend/tests/`: backend regression tests
- `frontend/src/`: React app, queue views, detail panes, and shared client types
- `docs/`: planning, architecture, features, changelog, and research synthesis
- `research/`: cloned reference repos used to shape the product roadmap

## Preferred Checks

Backend work:

- `cd backend && pytest -q`
- `cd backend && PYTHONPYCACHEPREFIX=/tmp/pycache python3 -m compileall app`

Frontend work:

- `cd frontend && npm run lint`
- `cd frontend && npm run build`

## Guidance For Agents

- keep backend and frontend contracts in sync
- surface backend capabilities in the UI before inventing new ones
- favor concise, inspectable markdown guidance files
- when updating docs, keep them aligned with the code that actually ships
