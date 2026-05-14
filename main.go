package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
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
	Type               string  `json:"type"`
	TotalElevationGain float64 `json:"total_elevation_gain"`
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

var runTypes = map[string]bool{
	"Run": true, "TrailRun": true, "Walk": true, "Hike": true, "VirtualRun": true,
}

var db *pgxpool.Pool

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

func getYearlyElevation(token string, year int, group string) (float64, error) {
	after := time.Date(year, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	before := time.Date(year+1, 1, 1, 0, 0, 0, 0, time.UTC).Unix()

	var total float64
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

		var activities []Activity
		if err := json.NewDecoder(resp.Body).Decode(&activities); err != nil {
			resp.Body.Close()
			return 0, fmt.Errorf("activities decode failed: %w", err)
		}
		resp.Body.Close()

		if len(activities) == 0 {
			break
		}

		for _, a := range activities {
			if activityGroup(a.Type) == group {
				total += a.TotalElevationGain
			}
		}

		page++
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
	return out + "m"
}

func buildComment(activityType string, yearly float64, year int) string {
	if strings.Contains(activityType, "Ride") {
		return fmt.Sprintf("🚴‍♂️⬆️ %d = %s | by climbprint", year, formatElev(yearly))
	}
	return fmt.Sprintf("🦶⬆️ %d = %s | by climbprint", year, formatElev(yearly))
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

func processActivity(athleteID, activityID int64) {
	ctx := context.Background()

	token, err := getAccessToken(ctx, athleteID)
	if err != nil {
		log.Printf("processActivity %d: getAccessToken: %v", activityID, err)
		return
	}

	activity, err := getActivity(activityID, token)
	if err != nil {
		log.Printf("processActivity %d: getActivity: %v", activityID, err)
		return
	}

	group := activityGroup(activity.Type)
	if group == "other" {
		log.Printf("processActivity %d: unsupported activity type %q, skipping", activityID, activity.Type)
		return
	}

	year := time.Now().Year()
	yearly, err := getYearlyElevation(token, year, group)
	if err != nil {
		log.Printf("processActivity %d: getYearlyElevation: %v", activityID, err)
		return
	}

	comment := buildComment(activity.Type, yearly, year)
	if err := updateDescription(activityID, comment, token); err != nil {
		log.Printf("processActivity %d: updateDescription: %v", activityID, err)
		return
	}

	log.Printf("processActivity %d: description updated (athlete=%d, type=%s, yearly=%.0fm, this=%.0fm)",
		activityID, athleteID, activity.Type, yearly, activity.TotalElevationGain)
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

		go processActivity(event.OwnerID, event.ObjectID)
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

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/webhook", webhookHandler)
	http.HandleFunc("/health", healthHandler)

	log.Printf("Listening on :%s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
