# climbprint

Strava webhook server that posts yearly elevation totals as a comment on each new activity.

## Prerequisites

- Go 1.22+
- A Strava API application
- Railway account (for deployment)

## Strava App Setup

1. Go to https://www.strava.com/settings/api and create an app
2. Note your **Client ID** and **Client Secret**

## Getting a Refresh Token

1. In your browser, open:
   ```
   https://www.strava.com/oauth/authorize?client_id=YOUR_CLIENT_ID&redirect_uri=http://localhost&response_type=code&scope=activity:read_all,activity:write
   ```
2. Authorize the app; copy the `code` param from the redirect URL
3. Exchange for tokens:
   ```sh
   curl -X POST https://www.strava.com/oauth/token \
     -d client_id=YOUR_CLIENT_ID \
     -d client_secret=YOUR_CLIENT_SECRET \
     -d code=YOUR_CODE \
     -d grant_type=authorization_code
   ```
4. Save the `refresh_token` from the response

## Local Setup

```sh
cp .env.example .env
# Fill in your values in .env
go build -o climbprint .
./climbprint
```

## Railway Deployment

1. Push this repo to GitHub
2. Create a new Railway project linked to your repo
3. Set environment variables in Railway dashboard (same keys as `.env.example`)
4. Railway will build and deploy automatically using the `Procfile`

## Registering the Webhook

After your server is deployed and running, register the Strava webhook subscription:

```sh
curl -X POST https://www.strava.com/api/v3/push_subscriptions \
  -d client_id=YOUR_CLIENT_ID \
  -d client_secret=YOUR_CLIENT_SECRET \
  -d callback_url=https://YOUR_RAILWAY_URL/webhook \
  -d verify_token=YOUR_WEBHOOK_VERIFY_TOKEN
```

## Endpoints

| Method | Path       | Description                        |
|--------|------------|------------------------------------|
| GET    | /webhook   | Strava subscription verification   |
| POST   | /webhook   | Receive activity events            |
| GET    | /health    | Health check                       |
