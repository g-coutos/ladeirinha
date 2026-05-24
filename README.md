# ladeirinha

Strava webhook server that automatically writes your yearly elevation total into the description of each new run or ride.

## Why it exists

Every time you log an activity on Strava, ladeirinha computes how much elevation you've climbed so far this year (for that activity type) and stamps it into the activity description — so you always know where you stand without opening a spreadsheet.

## Key features

- **Automatic descriptions** — writes `⛰️ 2025 = 12,340 m | 👉 ladeirinha.com.br` (runs) or `🚵 2025 = 8,210 m | 👉 ladeirinha.com.br` (rides) on every new activity
- **Per-group totals** — runs (Run, TrailRun, Walk, Hike, VirtualRun) and rides (anything containing "Ride") are tracked independently
- **Multi-athlete** — each athlete authorizes once via OAuth; tokens are stored and rotated automatically
- **Reliable job queue** — events are persisted to PostgreSQL with up to 5 retries and 30s backoff; stuck jobs are recovered on restart
- **Zero downtime deploys** — DB schema is created automatically on startup

## Tech stack

- **Go** (single `main.go`)
- **PostgreSQL** via `pgx/v5`
- **Strava API v3** (webhook + activities + OAuth)
- Deployed on **Railway**

## Getting started

### Prerequisites

- Go 1.22+
- PostgreSQL database
- Strava API application ([create one here](https://www.strava.com/settings/api))

### Installation

```sh
git clone <repo>
cd ladeirinha
cp .env.example .env
# fill in your values
go build -o ladeirinha .
./ladeirinha
```

### Environment variables

| Variable | Description |
|---|---|
| `DATABASE_URL` | PostgreSQL connection string |
| `STRAVA_CLIENT_ID` | Strava app client ID |
| `STRAVA_CLIENT_SECRET` | Strava app client secret |
| `STRAVA_REFRESH_TOKEN` | Seed refresh token for the first athlete |
| `STRAVA_WEBHOOK_VERIFY_TOKEN` | Token used to verify Strava webhook subscription |
| `STRAVA_REDIRECT_URI` | OAuth callback URL (e.g. `https://your-domain/auth/callback`) |
| `PORT` | HTTP port (default: `8080`) |

### Getting a refresh token (first athlete)

```sh
# 1. Open in browser and authorize:
https://www.strava.com/oauth/authorize?client_id=YOUR_CLIENT_ID&redirect_uri=http://localhost&response_type=code&scope=activity:read_all,activity:write

# 2. Exchange the code from the redirect URL:
curl -X POST https://www.strava.com/oauth/token \
  -d client_id=YOUR_CLIENT_ID \
  -d client_secret=YOUR_CLIENT_SECRET \
  -d code=YOUR_CODE \
  -d grant_type=authorization_code

# 3. Copy refresh_token into STRAVA_REFRESH_TOKEN in .env
```

### Registering the Strava webhook

```sh
curl -X POST https://www.strava.com/api/v3/push_subscriptions \
  -d client_id=YOUR_CLIENT_ID \
  -d client_secret=YOUR_CLIENT_SECRET \
  -d callback_url=https://YOUR_DOMAIN/webhook \
  -d verify_token=YOUR_WEBHOOK_VERIFY_TOKEN
```

## Project structure

```
main.go        # entire application — HTTP handlers, job queue, Strava API calls
Procfile       # Railway process definition (runs ./ladeirinha)
railway.toml   # Railway build config
.env.example   # environment variable template
```

## HTTP endpoints

| Method | Path | Description |
|---|---|---|
| GET | `/auth` | Redirects to Strava OAuth authorization page |
| GET | `/auth/callback` | Handles OAuth callback, saves refresh token |
| GET | `/webhook` | Strava subscription verification |
| POST | `/webhook` | Receives activity events, enqueues jobs |
| GET | `/health` | Health check |

## Deployment (Railway)

1. Push this repo to GitHub
2. Create a new Railway project linked to the repo
3. Set all environment variables in the Railway dashboard
4. Railway builds with `go build -o ladeirinha .` and runs via `Procfile` automatically
