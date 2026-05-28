package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
)

type StravaEvent struct {
	ObjectType string `json:"object_type"`
	AspectType string `json:"aspect_type"`
	ObjectID   int64  `json:"object_id"`
	OwnerID    int64  `json:"owner_id"`
}

type Activity struct {
	ID                 int64   `json:"id"`
	Type               string  `json:"type"`
	StartDate          string  `json:"start_date"`
	TotalElevationGain float64 `json:"total_elevation_gain"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Athlete      struct {
		ID int64 `json:"id"`
	} `json:"athlete"`
}

var runTypes = map[string]bool{
	"Run": true, "TrailRun": true, "Walk": true, "Hike": true, "VirtualRun": true,
}

var db *pgxpool.Pool

var errActivityNotFound = errors.New("target activity not found in list")

const (
	maxJobAttempts = 5
	retryDelay     = 30 * time.Second
)

func activityGroup(activityType string) string {
	if strings.Contains(activityType, "Ride") {
		return "ride"
	}
	if runTypes[activityType] {
		return "run"
	}
	return "other"
}

// getRefreshToken fetches the refresh token for the given athlete from the DB.
// If the athlete is not in the DB yet, it seeds from STRAVA_REFRESH_TOKEN env var.
func getRefreshToken(ctx context.Context, athleteID int64) (string, error) {
	var token string
	err := db.QueryRow(ctx,
		`SELECT refresh_token FROM athletes WHERE athlete_id = $1`, athleteID,
	).Scan(&token)

	if err == nil {
		return token, nil
	}

	// Athlete not found — seed from env var
	seed := os.Getenv("STRAVA_REFRESH_TOKEN")
	if seed == "" {
		return "", fmt.Errorf("athlete %d not found in DB and STRAVA_REFRESH_TOKEN is not set", athleteID)
	}

	_, err = db.Exec(ctx,
		`INSERT INTO athletes (athlete_id, refresh_token) VALUES ($1, $2)
		 ON CONFLICT (athlete_id) DO NOTHING`,
		athleteID, seed,
	)
	if err != nil {
		return "", fmt.Errorf("seed athlete %d: %w", athleteID, err)
	}

	log.Printf("athlete %d seeded from STRAVA_REFRESH_TOKEN env var", athleteID)
	return seed, nil
}

// saveRefreshToken persists the new refresh token returned by Strava.
func saveRefreshToken(ctx context.Context, athleteID int64, token string) error {
	_, err := db.Exec(ctx,
		`INSERT INTO athletes (athlete_id, refresh_token, updated_at)
		 VALUES ($1, $2, NOW())
		 ON CONFLICT (athlete_id) DO UPDATE
		 SET refresh_token = EXCLUDED.refresh_token, updated_at = NOW()`,
		athleteID, token,
	)
	return err
}

func getAccessToken(ctx context.Context, athleteID int64) (string, error) {
	refreshToken, err := getRefreshToken(ctx, athleteID)
	if err != nil {
		return "", err
	}

	data := url.Values{}
	data.Set("client_id", os.Getenv("STRAVA_CLIENT_ID"))
	data.Set("client_secret", os.Getenv("STRAVA_CLIENT_SECRET"))
	data.Set("refresh_token", refreshToken)
	data.Set("grant_type", "refresh_token")

	resp, err := http.PostForm("https://www.strava.com/oauth/token", data)
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("token request returned %d: %s", resp.StatusCode, body)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", fmt.Errorf("token decode failed: %w", err)
	}

	if err := saveRefreshToken(ctx, athleteID, tr.RefreshToken); err != nil {
		log.Printf("athlete %d: failed to save new refresh token: %v", athleteID, err)
	}

	return tr.AccessToken, nil
}

func getActivity(id int64, token string) (*Activity, error) {
	req, err := http.NewRequest("GET", fmt.Sprintf("https://www.strava.com/api/v3/activities/%d", id), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get activity request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("get activity returned %d: %s", resp.StatusCode, body)
	}

	var a Activity
	if err := json.NewDecoder(resp.Body).Decode(&a); err != nil {
		return nil, fmt.Errorf("activity decode failed: %w", err)
	}
	return &a, nil
}

func getYearlyElevation(token string, year int, group string, activityID int64) (float64, error) {
	after := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	before := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	var all []Activity
	page := 1

	for {
		reqURL := fmt.Sprintf(
			"https://www.strava.com/api/v3/athlete/activities?after=%d&before=%d&per_page=200&page=%d",
			after, before, page,
		)
		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Authorization", "Bearer "+token)

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, fmt.Errorf("activities request failed: %w", err)
		}

		if resp.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			return 0, fmt.Errorf("activities returned %d: %s", resp.StatusCode, body)
		}

		var page_activities []Activity
		if err := json.NewDecoder(resp.Body).Decode(&page_activities); err != nil {
			resp.Body.Close()
			return 0, fmt.Errorf("activities decode failed: %w", err)
		}
		resp.Body.Close()

		if len(page_activities) == 0 {
			break
		}

		all = append(all, page_activities...)
		page++
	}

	// Sort ascending by start_date so we accumulate in chronological order.
	// Strava returns ISO 8601 dates which are lexicographically sortable.
	sort.Slice(all, func(i, j int) bool {
		return all[i].StartDate < all[j].StartDate
	})

	var total float64
	found := false
	for _, a := range all {
		if activityGroup(a.Type) != group {
			continue
		}
		total += a.TotalElevationGain
		if a.ID == activityID {
			found = true
			break
		}
	}

	if !found {
		return 0, errActivityNotFound
	}

	return total, nil
}

func formatElev(m float64) string {
	s := fmt.Sprintf("%d", int(m))
	cut := len(s) % 3
	if cut == 0 {
		cut = 3
	}
	out := s[:cut]
	for i := cut; i < len(s); i += 3 {
		out += "," + s[i:i+3]
	}
	return out + " m"
}

func buildComment(activityType string, yearly float64, year int) string {
	if strings.Contains(activityType, "Ride") {
		return fmt.Sprintf("🚵 %d = %s | 👉 ladeirinha.com.br | (IT'S FREE)", year, formatElev(yearly))
	}
	return fmt.Sprintf("⛰️ %d = %s | 👉 ladeirinha.com.br | (IT'S FREE)", year, formatElev(yearly))
}

func updateDescription(activityID int64, text, token string) error {
	body, err := json.Marshal(map[string]string{"description": text})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(
		"PUT",
		fmt.Sprintf("https://www.strava.com/api/v3/activities/%d", activityID),
		bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("update description request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("update description returned %d: %s", resp.StatusCode, respBody)
	}
	return nil
}

func enqueueJob(ctx context.Context, athleteID, activityID int64) error {
	_, err := db.Exec(ctx,
		`INSERT INTO jobs (athlete_id, activity_id) VALUES ($1, $2)`,
		athleteID, activityID,
	)
	return err
}

func runNextJob(ctx context.Context) {
	tx, err := db.Begin(ctx)
	if err != nil {
		log.Printf("worker: begin tx: %v", err)
		return
	}

	var jobID, athleteID, activityID int64
	var attempts int
	err = tx.QueryRow(ctx, `
		SELECT id, athlete_id, activity_id, attempts
		FROM jobs
		WHERE status = 'pending' AND scheduled_at <= NOW()
		ORDER BY scheduled_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED
	`).Scan(&jobID, &athleteID, &activityID, &attempts)
	if err != nil {
		tx.Rollback(ctx)
		return // no jobs ready
	}

	if _, err = tx.Exec(ctx, `UPDATE jobs SET status = 'processing' WHERE id = $1`, jobID); err != nil {
		tx.Rollback(ctx)
		return
	}
	tx.Commit(ctx)

	attempts++
	if err := processActivity(athleteID, activityID); err != nil {
		if attempts >= maxJobAttempts {
			log.Printf("worker: job %d failed permanently after %d attempts: %v", jobID, attempts, err)
			db.Exec(ctx, `UPDATE jobs SET status = 'failed', attempts = $1 WHERE id = $2`, attempts, jobID)
		} else {
			next := time.Now().Add(retryDelay)
			db.Exec(ctx, `UPDATE jobs SET status = 'pending', attempts = $1, scheduled_at = $2 WHERE id = $3`,
				attempts, next, jobID)
			log.Printf("worker: job %d attempt %d failed, retry at %v: %v", jobID, attempts, next, err)
		}
		return
	}

	db.Exec(ctx, `UPDATE jobs SET status = 'done', attempts = $1 WHERE id = $2`, attempts, jobID)
}

func startWorker(ctx context.Context) {
	// Reset jobs stuck in processing (e.g. from a previous crash).
	if _, err := db.Exec(ctx, `
		UPDATE jobs SET status = 'pending', scheduled_at = NOW()
		WHERE status = 'processing'
	`); err != nil {
		log.Printf("worker: reset stuck jobs: %v", err)
	}

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runNextJob(ctx)
		}
	}
}

func processActivity(athleteID, activityID int64) error {
	ctx := context.Background()

	token, err := getAccessToken(ctx, athleteID)
	if err != nil {
		return fmt.Errorf("getAccessToken: %w", err)
	}

	activity, err := getActivity(activityID, token)
	if err != nil {
		return fmt.Errorf("getActivity: %w", err)
	}

	group := activityGroup(activity.Type)
	if group == "other" {
		log.Printf("processActivity %d: unsupported activity type %q, skipping", activityID, activity.Type)
		return nil
	}

	year := time.Now().Year()
	yearly, err := getYearlyElevation(token, year, group, activityID)
	if err != nil {
		return fmt.Errorf("getYearlyElevation: %w", err)
	}

	comment := buildComment(activity.Type, yearly, year)
	if err := updateDescription(activityID, comment, token); err != nil {
		return fmt.Errorf("updateDescription: %w", err)
	}

	log.Printf("processActivity %d: description updated (athlete=%d, type=%s, yearly=%.0fm, this=%.0fm)",
		activityID, athleteID, activity.Type, yearly, activity.TotalElevationGain)
	return nil
}

func authHandler(w http.ResponseWriter, r *http.Request) {
	authURL := fmt.Sprintf(
		"https://www.strava.com/oauth/authorize?client_id=%s&redirect_uri=%s&response_type=code&scope=activity:read_all,activity:write",
		os.Getenv("STRAVA_CLIENT_ID"),
		url.QueryEscape(os.Getenv("STRAVA_REDIRECT_URI")),
	)
	http.Redirect(w, r, authURL, http.StatusFound)
}

func authCallbackHandler(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "missing code", http.StatusBadRequest)
		return
	}

	data := url.Values{}
	data.Set("client_id", os.Getenv("STRAVA_CLIENT_ID"))
	data.Set("client_secret", os.Getenv("STRAVA_CLIENT_SECRET"))
	data.Set("code", code)
	data.Set("grant_type", "authorization_code")

	resp, err := http.PostForm("https://www.strava.com/oauth/token", data)
	if err != nil {
		http.Error(w, "token request failed", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("authCallback: strava token error %d: %s", resp.StatusCode, body)
		http.Error(w, "strava authorization failed", http.StatusBadGateway)
		return
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		http.Error(w, "token decode failed", http.StatusInternalServerError)
		return
	}

	if err := saveRefreshToken(r.Context(), tr.Athlete.ID, tr.RefreshToken); err != nil {
		log.Printf("authCallback: saveRefreshToken athlete %d: %v", tr.Athlete.ID, err)
		http.Error(w, "failed to save token", http.StatusInternalServerError)
		return
	}

	log.Printf("authCallback: athlete %d authorized", tr.Athlete.ID)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<html><body><p>Autorizado com sucesso! Suas atividades serão atualizadas automaticamente.</p></body></html>`)
}

func webhookHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mode := r.URL.Query().Get("hub.mode")
		challenge := r.URL.Query().Get("hub.challenge")
		verifyToken := r.URL.Query().Get("hub.verify_token")

		if mode != "subscribe" || verifyToken != os.Getenv("STRAVA_WEBHOOK_VERIFY_TOKEN") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"hub.challenge": challenge})

	case http.MethodPost:
		var event StravaEvent
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if event.ObjectType != "activity" || event.AspectType != "create" {
			json.NewEncoder(w).Encode(map[string]string{"status": "ignored"})
			return
		}

		if err := enqueueJob(r.Context(), event.OwnerID, event.ObjectID); err != nil {
			log.Printf("webhookHandler: enqueueJob: %v", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("No .env file found, using environment variables")
	}

	ctx := context.Background()

	var err error
	db, err = pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer db.Close()

	if _, err = db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS athletes (
			athlete_id    BIGINT PRIMARY KEY,
			refresh_token TEXT NOT NULL,
			updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		log.Fatalf("failed to create athletes table: %v", err)
	}

	if _, err = db.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS jobs (
			id           BIGSERIAL PRIMARY KEY,
			athlete_id   BIGINT NOT NULL,
			activity_id  BIGINT NOT NULL,
			status       TEXT NOT NULL DEFAULT 'pending',
			attempts     INT NOT NULL DEFAULT 0,
			scheduled_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)
	`); err != nil {
		log.Fatalf("failed to create jobs table: %v", err)
	}

	go startWorker(ctx)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/auth", authHandler)
	http.HandleFunc("/auth/callback", authCallbackHandler)
	http.HandleFunc("/webhook", webhookHandler)
	http.HandleFunc("/health", healthHandler)

	log.Printf("Listening on :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
