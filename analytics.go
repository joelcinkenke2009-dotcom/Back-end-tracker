package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
)

type conversionPayload struct {
	EventName string  `json:"event_name"`
	Value     float64 `json:"value"`
	Currency  string  `json:"currency"`
}

func (e *Env) ensureAnalyticsTables() error {
	_, err := e.db.Exec(`CREATE TABLE IF NOT EXISTS tracker_events (
		id BIGINT AUTO_INCREMENT PRIMARY KEY,
		user_id VARCHAR(255) NOT NULL,
		slug VARCHAR(250) NOT NULL,
		event_type VARCHAR(24) NOT NULL,
		event_name VARCHAR(80) NOT NULL DEFAULT '',
		event_value DECIMAL(12,2) NOT NULL DEFAULT 0,
		currency VARCHAR(8) NOT NULL DEFAULT '',
		visitor_hash CHAR(64) NOT NULL,
		ip_hash CHAR(64) NOT NULL,
		user_agent VARCHAR(512) NOT NULL DEFAULT '',
		referrer TEXT,
		utm_source VARCHAR(160) NOT NULL DEFAULT '',
		utm_medium VARCHAR(160) NOT NULL DEFAULT '',
		utm_campaign VARCHAR(160) NOT NULL DEFAULT '',
		is_bot BOOLEAN NOT NULL DEFAULT FALSE,
		is_duplicate BOOLEAN NOT NULL DEFAULT FALSE,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_tracker_events_user_date (user_id, created_at),
		INDEX idx_tracker_events_user_type_date (user_id, event_type, created_at),
		INDEX idx_tracker_events_slug_date (slug, created_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	return err
}

func hashTrackingValue(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func trackingVisitor(r *http.Request) (string, string) {
	ip := clientIP(r)
	ua := r.UserAgent()
	return hashTrackingValue(ip + "|" + ua), hashTrackingValue(ip)
}

func trackingUTM(r *http.Request, key string) string {
	value := strings.TrimSpace(r.URL.Query().Get(key))
	if len(value) > 160 {
		return value[:160]
	}
	return value
}

func (e *Env) insertTrackingEvent(r *http.Request, userID, slug, eventType, eventName string, value float64, currency string, isBot, duplicate bool) error {
	visitorHash, ipHash := trackingVisitor(r)
	referrer := r.Referer()
	if len(referrer) > 2000 {
		referrer = referrer[:2000]
	}
	ua := r.UserAgent()
	if len(ua) > 512 {
		ua = ua[:512]
	}
	_, err := e.db.ExecContext(r.Context(), `INSERT INTO tracker_events
		(user_id, slug, event_type, event_name, event_value, currency, visitor_hash, ip_hash, user_agent, referrer, utm_source, utm_medium, utm_campaign, is_bot, is_duplicate)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		userID, slug, eventType, eventName, value, currency, visitorHash, ipHash, ua, referrer,
		trackingUTM(r, "utm_source"), trackingUTM(r, "utm_medium"), trackingUTM(r, "utm_campaign"), isBot, duplicate)
	if err == nil && e.cache != nil {
		e.cache.DeletePrefix("analytics:" + userID + ":")
	}
	return err
}

// redirectWithAnalytics preserves the existing short-link behaviour while recording
// privacy-conscious event metadata for live analytics and traffic-quality analysis.
func (e *Env) redirectWithAnalytics(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")
	target, userID, err := e.resolveTrackedLink(r.Context(), slug)
	if err != nil {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	bot := isBotRequest(r)
	duplicate := false
	if !bot {
		duplicate = isDuplicateClick(slug, r)
	}
	clickAllowed := false
	if !bot && !duplicate {
		clickAllowed, err = e.consumeClickQuota(userID)
		if err != nil {
			fmt.Println("subscription click quota check failed:", err)
		}
	}
	if bot || duplicate || clickAllowed {
		if err := e.insertTrackingEvent(r, userID, slug, "click", "", 0, "", bot, duplicate); err != nil {
			// Tracking failures must never prevent a visitor from reaching the destination.
			fmt.Println("analytics event insert failed:", err)
		}
	}

	if clickAllowed {
		query := "UPDATE link_Tracker_Link SET click_total=click_total+1"
		if isMobileRequest(r) {
			query += ", click_mobile=click_mobile+1"
		} else {
			query += ", click_pc=click_pc+1"
		}
		if _, err := e.db.ExecContext(r.Context(), query+" WHERE slug=?", slug); err != nil {
			fmt.Println("analytics click counter update failed:", err)
		} else if e.cache != nil {
			e.cache.DeletePrefix("analytics:" + userID + ":")
		}
	}
	http.Redirect(w, r, target, http.StatusFound)
}

func analyticsUser(r *http.Request) (string, bool) {
	user, ok := r.Context().Value(userInfosKey).(string)
	return user, ok && user != ""
}

func (e *Env) analyticsSummary(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")
	user, ok := analyticsUser(r)
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	// These four reads are independent. Running them concurrently removes the
	// serial database wait from the analytics summary without changing its JSON.
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	type dailyPoint struct {
		Date        string `json:"date"`
		Clicks      int64  `json:"clicks"`
		Conversions int64  `json:"conversions"`
	}
	var (
		clicks, clicks30d, mobile, desktop, conversions, suspicious int64
		daily = make([]dailyPoint, 0)
		errs [5]error
		wg sync.WaitGroup
	)
	wg.Add(5)

	go func() {
		defer wg.Done()
		errs[0] = e.db.QueryRowContext(ctx, `SELECT COALESCE(SUM(click_total),0),
			COALESCE(SUM(click_mobile),0), COALESCE(SUM(click_pc),0)
			FROM link_Tracker_Link WHERE user_id=?`, user).Scan(&clicks, &mobile, &desktop)
	}()
	go func() {
		defer wg.Done()
		errs[1] = e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracker_events
			WHERE user_id=? AND event_type='click' AND is_bot=0 AND is_duplicate=0
			AND created_at >= NOW() - INTERVAL 30 DAY`, user).Scan(&clicks30d)
	}()
	go func() {
		defer wg.Done()
		errs[2] = e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracker_events
			WHERE user_id=? AND event_type='conversion' AND is_bot=0 AND is_duplicate=0
			AND created_at >= NOW() - INTERVAL 30 DAY`, user).Scan(&conversions)
	}()
	go func() {
		defer wg.Done()
		errs[3] = e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM tracker_events
			WHERE user_id=? AND (is_bot=1 OR is_duplicate=1)
			AND created_at >= NOW() - INTERVAL 30 DAY`, user).Scan(&suspicious)
	}()
	go func() {
		defer wg.Done()
		rows, err := e.db.QueryContext(ctx, `SELECT DATE(created_at),
			SUM(CASE WHEN event_type='click' THEN 1 ELSE 0 END),
			SUM(CASE WHEN event_type='conversion' THEN 1 ELSE 0 END)
			FROM tracker_events WHERE user_id=? AND is_bot=0 AND is_duplicate=0
			AND created_at >= NOW() - INTERVAL 14 DAY
			GROUP BY DATE(created_at) ORDER BY DATE(created_at)`, user)
		if err != nil {
			errs[4] = err
			return
		}
		defer rows.Close()
		for rows.Next() {
			var point dailyPoint
			if err := rows.Scan(&point.Date, &point.Clicks, &point.Conversions); err != nil {
				errs[3] = err
				return
			}
			daily = append(daily, point)
		}
		errs[4] = rows.Err()
	}()

	wg.Wait()
	for _, err := range errs {
		if err != nil {
			log.Printf("analytics summary query failed: user_id=%q: %v", user, err)
			http.Error(w, `{"error":"analytics_unavailable"}`, http.StatusInternalServerError)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"clicks": clicks, "clicks_30d": clicks30d, "mobile": mobile, "desktop": desktop,
		"conversions": conversions, "suspicious": suspicious, "daily": daily,
	})
}

func (e *Env) analyticsLive(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")
	user, ok := analyticsUser(r)
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	rows, err := e.db.QueryContext(r.Context(), `SELECT COALESCE(slug,''), COALESCE(event_type,''), COALESCE(event_name,''), created_at, COALESCE(is_bot,0), COALESCE(is_duplicate,0), COALESCE(utm_source,''), COALESCE(referrer,'')
		FROM tracker_events WHERE user_id=? AND created_at >= NOW() - INTERVAL 24 HOUR
		ORDER BY created_at DESC, id DESC LIMIT 50`, user)
	if err != nil {
		log.Printf("analytics live query failed: user_id=%q: %v", user, err)
		http.Error(w, `{"error":"live_unavailable"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type liveEvent struct {
		Slug        string    `json:"slug"`
		Type        string    `json:"type"`
		Name        string    `json:"name"`
		At          time.Time `json:"at"`
		Bot         bool      `json:"bot"`
		Duplicate   bool      `json:"duplicate"`
		UTMSource   string    `json:"utm_source"`
		Referrer    string    `json:"referrer"`
	}
	events := make([]liveEvent, 0)
	for rows.Next() {
		var event liveEvent
		if err := rows.Scan(&event.Slug, &event.Type, &event.Name, &event.At, &event.Bot, &event.Duplicate, &event.UTMSource, &event.Referrer); err != nil {
			log.Printf("analytics live scan failed: user_id=%q: %v", user, err)
			http.Error(w, `{"error":"live_unavailable"}`, http.StatusInternalServerError)
			return
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		log.Printf("analytics live iteration failed: user_id=%q: %v", user, err)
		http.Error(w, `{"error":"live_unavailable"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"events": events, "updated_at": time.Now().UTC()})
}

func (e *Env) analyticsFraud(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")
	user, ok := analyticsUser(r)
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	rows, err := e.db.QueryContext(r.Context(), `SELECT slug, COUNT(*),
		SUM(CASE WHEN is_bot=1 THEN 1 ELSE 0 END),
		SUM(CASE WHEN is_duplicate=1 THEN 1 ELSE 0 END), MAX(created_at)
		FROM tracker_events WHERE user_id=? AND (is_bot=1 OR is_duplicate=1)
		AND created_at >= NOW() - INTERVAL 30 DAY
		GROUP BY slug ORDER BY COUNT(*) DESC LIMIT 20`, user)
	if err != nil {
		log.Printf("analytics fraud query failed: user_id=%q: %v", user, err)
		http.Error(w, `{"error":"fraud_unavailable"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type alert struct {
		Slug       string    `json:"slug"`
		Total      int64     `json:"total"`
		Bots       int64     `json:"bots"`
		Repeated   int64     `json:"repeated"`
		LastSeen   time.Time `json:"last_seen"`
	}
	alerts := make([]alert, 0)
	for rows.Next() {
		var item alert
		if err := rows.Scan(&item.Slug, &item.Total, &item.Bots, &item.Repeated, &item.LastSeen); err != nil {
			log.Printf("analytics fraud scan failed: user_id=%q: %v", user, err)
			http.Error(w, `{"error":"fraud_unavailable"}`, http.StatusInternalServerError)
			return
		}
		alerts = append(alerts, item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("analytics fraud iteration failed: user_id=%q: %v", user, err)
		http.Error(w, `{"error":"fraud_unavailable"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"alerts": alerts})
}

func (e *Env) recordConversion(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	slug := chi.URLParam(r, "slug")
	_, userID, err := e.resolveTrackedLink(r.Context(), slug)
	if err != nil {
		http.Error(w, `{"error":"unknown_link"}`, http.StatusNotFound)
		return
	}
	if err := e.ensureUserSubscription(userID); err != nil {
		http.Error(w, `{"error":"subscription_unavailable"}`, http.StatusInternalServerError)
		return
	}
	var analyticsAllowed bool
	if err := e.db.QueryRowContext(r.Context(), `SELECT analytics_enabled FROM user_subscriptions
		WHERE user_id=? AND (expires_at IS NULL OR expires_at > NOW())`, userID).Scan(&analyticsAllowed); err != nil || !analyticsAllowed {
		http.Error(w, `{"error":"analytics_plan_required"}`, http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var payload conversionPayload
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, `{"error":"invalid_payload"}`, http.StatusBadRequest)
		return
	}
	payload.EventName = strings.TrimSpace(payload.EventName)
	if payload.EventName == "" {
		payload.EventName = "purchase"
	}
	if len(payload.EventName) > 80 || payload.Value < 0 {
		http.Error(w, `{"error":"invalid_conversion"}`, http.StatusBadRequest)
		return
	}
	payload.Currency = strings.ToUpper(strings.TrimSpace(payload.Currency))
	if len(payload.Currency) > 8 {
		payload.Currency = payload.Currency[:8]
	}
	if err := e.insertTrackingEvent(r, userID, slug, "conversion", payload.EventName, payload.Value, payload.Currency, false, false); err != nil {
		log.Printf("analytics public conversion insert failed: user_id=%q slug=%q: %v", userID, slug, err)
		http.Error(w, `{"error":"conversion_unavailable"}`, http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"recorded": true})
}

func (e *Env) analyticsConversions(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	user, ok := analyticsUser(r)
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodPost {
		r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
		var payload struct {
			Slug      string  `json:"slug"`
			EventName string  `json:"event_name"`
			Value     float64 `json:"value"`
			Currency  string  `json:"currency"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || strings.TrimSpace(payload.Slug) == "" {
			http.Error(w, `{"error":"invalid_payload"}`, http.StatusBadRequest)
			return
		}
		var exists int
		if err := e.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM link_Tracker_Link WHERE slug=? AND user_id=?", payload.Slug, user).Scan(&exists); err != nil || exists == 0 {
			http.Error(w, `{"error":"unknown_link"}`, http.StatusNotFound)
			return
		}
		payload.EventName = strings.TrimSpace(payload.EventName)
		if payload.EventName == "" { payload.EventName = "purchase" }
		if len(payload.EventName) > 80 || payload.Value < 0 {
			http.Error(w, `{"error":"invalid_conversion"}`, http.StatusBadRequest)
			return
		}
		payload.Currency = strings.ToUpper(strings.TrimSpace(payload.Currency))
		if len(payload.Currency) > 8 { payload.Currency = payload.Currency[:8] }
		if err := e.insertTrackingEvent(r, user, payload.Slug, "conversion", payload.EventName, payload.Value, payload.Currency, false, false); err != nil {
			log.Printf("analytics manual conversion insert failed: user_id=%q slug=%q: %v", user, payload.Slug, err)
			http.Error(w, `{"error":"conversion_unavailable"}`, http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"recorded": true})
		return
	}
	rows, err := e.db.QueryContext(r.Context(), `SELECT slug,event_name,event_value,currency,created_at FROM tracker_events
		WHERE user_id=? AND event_type='conversion' ORDER BY created_at DESC, id DESC LIMIT 100`, user)
	if err != nil {
		log.Printf("analytics conversions query failed: user_id=%q: %v", user, err)
		http.Error(w, `{"error":"conversion_unavailable"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type conversion struct {
		Slug string `json:"slug"`
		Name string `json:"name"`
		Value float64 `json:"value"`
		Currency string `json:"currency"`
		At time.Time `json:"at"`
	}
	items := make([]conversion,0)
	for rows.Next() {
		var item conversion
		if err := rows.Scan(&item.Slug,&item.Name,&item.Value,&item.Currency,&item.At); err != nil {
			log.Printf("analytics conversions scan failed: user_id=%q: %v", user, err)
			http.Error(w, `{"error":"conversion_unavailable"}`, http.StatusInternalServerError)
			return
		}
		items=append(items,item)
	}
	if err := rows.Err(); err != nil {
		log.Printf("analytics conversions iteration failed: user_id=%q: %v", user, err)
		http.Error(w, `{"error":"conversion_unavailable"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"conversions":items})
}

func (e *Env) analyticsReport(w http.ResponseWriter, r *http.Request) {
	user, ok := analyticsUser(r)
	if !ok {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	rows, err := e.db.QueryContext(r.Context(), `SELECT slug,event_type,event_name,event_value,currency,utm_source,utm_medium,utm_campaign,created_at,is_bot,is_duplicate
		FROM tracker_events WHERE user_id=? AND created_at >= NOW() - INTERVAL 30 DAY ORDER BY id DESC`, user)
	if err != nil {
		log.Printf("analytics report query failed: user_id=%q: %v", user, err)
		http.Error(w, "Rapport indisponible", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="adpulse-rapport-30-jours.csv"`)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"Lien","Type","Evenement","Valeur","Devise","UTM source","UTM medium","UTM campagne","Date UTC","Bot","Repetition"})
	for rows.Next() {
		var slug,eventType,eventName,currency,source,medium,campaign string
		var value float64
		var at time.Time
		var bot,duplicate bool
		if err := rows.Scan(&slug,&eventType,&eventName,&value,&currency,&source,&medium,&campaign,&at,&bot,&duplicate); err != nil {
			log.Printf("analytics report scan failed: user_id=%q: %v", user, err)
			http.Error(w, "Rapport indisponible", http.StatusInternalServerError)
			return
		}
		_ = writer.Write([]string{slug,eventType,eventName,strconv.FormatFloat(value,'f',2,64),currency,source,medium,campaign,at.UTC().Format(time.RFC3339),strconv.FormatBool(bot),strconv.FormatBool(duplicate)})
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		fmt.Println("CSV report write failed:", err)
	}
}

