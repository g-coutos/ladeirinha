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

# Run tests (none exist yet)
go test ./...
```

## Architecture

Everything lives in `main.go`. The flow:

1. Strava POSTs to `/webhook` on activity create
2. `processActivity` runs in a goroutine: fetches an access token, fetches the activity, computes yearly elevation via paginated `/athlete/activities`, then PUTs the description back.

**Activity grouping** (`activityGroup`): activities are bucketed into `"run"` (Run, TrailRun, Walk, Hike, VirtualRun), `"ride"` (anything containing "Ride"), or `"other"` (skipped). Yearly elevation is computed only for the same group as the triggering activity.

**Comment text** (`buildComment`): hardcoded Portuguese strings differentiated by ride vs. run. Edit here to change what gets written to the activity description.

## Environment variables

| Variable | Description |
|---|---|
| `STRAVA_CLIENT_ID` | Strava app client ID |
| `STRAVA_CLIENT_SECRET` | Strava app client secret |
| `STRAVA_REFRESH_TOKEN` | OAuth refresh token with `activity:read_all,activity:write` |
| `STRAVA_WEBHOOK_VERIFY_TOKEN` | Token used to verify Strava webhook subscription |
| `PORT` | HTTP port (default: `8080`) |

## Deployment

Deployed on Railway. The `Procfile` runs the compiled binary `./climbprint`. Railway builds with `go build -o climbprint .` automatically.
