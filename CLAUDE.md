# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A single-file Go webhook server that listens for Strava activity creation events and updates each new activity's description with the athlete's yearly elevation total for that activity group (run or ride).

## Commands

```sh
# Build
go build -o climbprint .

# Run locally (requires .env)
./climbprint

# Run without building
go run .

# Run tests
go test ./...
```

## Architecture

Everything lives in `main.go`. The flow:

1. Strava POSTs to `/webhook` on activity create
2. `enqueueJob` inserts a row into the `jobs` table
3. A background worker (`startWorker`) polls the DB every 5s and calls `processActivity` for pending jobs
4. `processActivity`: fetches an access token, fetches the activity, computes yearly elevation via paginated `/athlete/activities`, then PUTs the description back

**Job queue**: DB-backed with status (`pending` / `processing` / `done` / `failed`), up to `maxJobAttempts` (5) retries with 30s delay. On startup, stuck `processing` jobs are reset to `pending`.

**Multi-athlete**: Each athlete has their own refresh token stored in the `athletes` table. On first event for an athlete not yet in the DB, the token is seeded from `STRAVA_REFRESH_TOKEN` env var. New athletes authorize via the OAuth flow at `/auth` → `/auth/callback`.

**Activity grouping** (`activityGroup`): activities are bucketed into `"run"` (Run, TrailRun, Walk, Hike, VirtualRun), `"ride"` (anything containing "Ride"), or `"other"` (skipped). Yearly elevation is computed only for the same group as the triggering activity, accumulated in chronological order up to and including the triggering activity.

**Comment text** (`buildComment`): hardcoded Portuguese strings differentiated by ride vs. run. Edit here to change what gets written to the activity description.

## Database schema

Two tables created automatically on startup (also in `schema.sql`):

- `athletes`: `athlete_id` (PK), `refresh_token`, `updated_at`
- `jobs`: `id`, `athlete_id`, `activity_id`, `status`, `attempts`, `scheduled_at`, `created_at`

## Environment variables

| Variable | Description |
|---|---|
| `DATABASE_URL` | PostgreSQL connection string |
| `STRAVA_CLIENT_ID` | Strava app client ID |
| `STRAVA_CLIENT_SECRET` | Strava app client secret |
| `STRAVA_REFRESH_TOKEN` | Seed refresh token for the first athlete |
| `STRAVA_WEBHOOK_VERIFY_TOKEN` | Token used to verify Strava webhook subscription |
| `STRAVA_REDIRECT_URI` | OAuth callback URL (e.g. `https://your-domain/auth/callback`) |
| `PORT` | HTTP port (default: `8080`) |

## HTTP endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/auth` | Redirects to Strava OAuth authorization page |
| GET | `/auth/callback` | Handles OAuth callback, saves refresh token |
| GET | `/webhook` | Strava subscription verification |
| POST | `/webhook` | Receives activity events, enqueues jobs |
| GET | `/health` | Health check |

## Deployment

Deployed on Railway. The `Procfile` runs the compiled binary `./climbprint`. Railway builds with `go build -o climbprint .` automatically via `railway.toml`.
