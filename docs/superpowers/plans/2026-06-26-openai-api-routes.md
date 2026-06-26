# OpenAI API Routes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace public `/api/v1/generate/*` routes with OpenAI-compatible `/v1/images/*` and `/v1/videos*` routes while preserving the existing async Comfy task engine.

**Architecture:** Add thin OpenAI-compatible HTTP handlers in `openai_api.go` that translate OpenAI request fields into existing `CreateTaskRequest` values. Keep route wiring in `routes.go`; register only `/v1/...`, `/outputs/...`, and `/healthz` HTTP routes.

**Tech Stack:** Go `net/http`, existing in-memory task queue, Markdown docs.

---

### Task 1: Route Contract Tests

**Files:**
- Modify: `main_test.go`

- [x] Add failing tests for `/v1/images/generations`, `/v1/images/edits`, `/v1/videos`, `/v1/videos/{id}`, `/v1/videos/{id}/content`, and removal of `/api/v1/generate/*`.
- [x] Run `go test ./...` and confirm failure before production changes.

### Task 2: OpenAI Route Handlers

**Files:**
- Create: `openai_api.go`
- Create: `routes.go`
- Modify: `main.go`

- [x] Add OpenAI request/response structs and small conversion helpers.
- [x] Add handlers for image generation, image edits, video creation, video lookup, video content redirect, and video deletion.
- [x] Register only `/v1/...` public API routes; do not register `/api/v1/*`.
- [x] Run `gofmt -w main.go openai_api.go routes.go main_test.go` and `go test ./...`.

### Task 3: Documentation And Migration Guide

**Files:**
- Modify: `README.md`
- Modify: `REST_API.md`
- Create: `API_MIGRATION.md`

- [x] Document the new OpenAI-compatible route list and request examples.
- [x] Add an old-to-new route mapping table and field migration notes.
- [x] Run `git diff --check`.

### Task 4: Final Verification

- [x] Run `go test ./...`.
- [x] Run `bash -n scripts/start_multi_stack.sh`.
- [x] Run `rg -n "/api/v1/generate|t2i-darkbeast6|darkbeast6|DarkBeast6" . -g '!chat_image' -g '!models/**' -g '!outputs/**' -g '!uploads/**'` and confirm only intentional migration-doc references remain.
