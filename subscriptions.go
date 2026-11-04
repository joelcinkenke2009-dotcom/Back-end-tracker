package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"
)

type subscriptionPlanConfig struct {
	LinksLimit     int64
	ClicksLimit    int64
	Analytics      bool
}

var subscriptionPlans = map[string]subscriptionPlanConfig{
	"free":      {LinksLimit: 1, ClicksLimit: 30, Analytics: false},
	"standard":  {LinksLimit: 5, ClicksLimit: 350, Analytics: false},
	"advanced":  {LinksLimit: 20, ClicksLimit: 1250, Analytics: true},
	"unlimited": {LinksLimit: -1, ClicksLimit: -1, Analytics: true},
}

// ensureSubscriptionTables installs the subscription table and gives every
// existing account the free plan. It is safe to run on every application start.
func (e *Env) ensureSubscriptionTables() error {
	_, err := e.db.Exec(`CREATE TABLE IF NOT EXISTS user_subscriptions (
		user_id VARCHAR(255) NOT NULL PRIMARY KEY,
		plan VARCHAR(24) NOT NULL DEFAULT 'free',
		status VARCHAR(24) NOT NULL DEFAULT 'active',
		links_limit INT NOT NULL DEFAULT 1,
		clicks_limit BIGINT NOT NULL DEFAULT 30,
		clicks_used BIGINT NOT NULL DEFAULT 0,
		analytics_enabled BOOLEAN NOT NULL DEFAULT FALSE,
		period_started_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
		expires_at DATETIME NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
		INDEX idx_subscription_expiry (status, expires_at)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	if err != nil {
		return err
	}
	_, err = e.db.Exec(`INSERT IGNORE INTO user_subscriptions (user_id)
		SELECT id FROM user_Tracker_Link WHERE id IS NOT NULL AND id <> ''`)
	if err != nil {
		return err
	}
	_, err = e.db.Exec(`CREATE TABLE IF NOT EXISTS pending_subscription_payments (
		cart_id VARCHAR(255) NOT NULL PRIMARY KEY,
		user_id VARCHAR(255) NOT NULL,
		plan VARCHAR(24) NOT NULL,
		expires_at DATETIME NOT NULL,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		INDEX idx_pending_subscription_user (user_id)
	) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4`)
	return err
}

// ensureUserSubscription creates a free plan for a legacy account and downgrades
// an expired paid plan without deleting any of the user's existing links.
func (e *Env) ensureUserSubscription(userID string) error {
	if userID == "" {
		return fmt.Errorf("missing user id")
	}
	if _, err := e.db.Exec("INSERT IGNORE INTO user_subscriptions (user_id) VALUES (?)", userID); err != nil {
		return err
	}
	_, err := e.db.Exec(`UPDATE user_subscriptions
		SET plan='free', status='active', links_limit=1, clicks_limit=30,
		    clicks_used=0, analytics_enabled=FALSE, period_started_at=NOW(), expires_at=NULL
		WHERE user_id=? AND plan <> 'free' AND expires_at IS NOT NULL AND expires_at <= NOW()`, userID)
	return err
}

func (e *Env) subscriptionStatus(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	userID := cookieRecuperation(w, r, "user_id")
	if userID == "nil" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	if err := e.ensureUserSubscription(userID); err != nil {
		http.Error(w, `{"error":"subscription_unavailable"}`, http.StatusInternalServerError)
		return
	}

	var plan, status string
	var linksLimit int64
	var clicksLimit, clicksUsed int64
	var analytics bool
	var expires sql.NullTime
	var linksUsed int64
	err := e.db.QueryRowContext(r.Context(), `SELECT s.plan, s.status, s.links_limit,
		s.clicks_limit, s.clicks_used, s.analytics_enabled, s.expires_at,
		(SELECT COUNT(*) FROM link_Tracker_Link l WHERE l.user_id=s.user_id)
		FROM user_subscriptions s WHERE s.user_id=?`, userID).Scan(
		&plan, &status, &linksLimit, &clicksLimit, &clicksUsed, &analytics, &expires, &linksUsed)
	if err != nil {
		http.Error(w, `{"error":"subscription_unavailable"}`, http.StatusInternalServerError)
		return
	}
	payload := map[string]any{
		"plan": plan, "status": status, "links_limit": linksLimit,
		"links_used": linksUsed, "clicks_limit": clicksLimit,
		"clicks_used": clicksUsed, "analytics_enabled": analytics,
		"links_remaining": remaining(linksLimit, linksUsed),
		"clicks_remaining": remaining(clicksLimit, clicksUsed),
	}
	if expires.Valid {
		payload["expires_at"] = expires.Time
		payload["date_limite"] = expires.Time.Local().Format("02 January 2006 à 15:04")
	}
	_ = json.NewEncoder(w).Encode(payload)
}

func remaining(limit, used int64) int64 {
	if limit < 0 {
		return -1
	}
	if used >= limit {
		return 0
	}
	return limit - used
}

// createLinkWithinPlan locks the subscription row and counts links in the same
// transaction, preventing parallel requests from bypassing the link allowance.
func (e *Env) createLinkWithinPlan(userID, target, slug, generatedURL string) (bool, error) {
	tx, err := e.db.Begin()
	if err != nil {
		return false, err
	}
	defer tx.Rollback()

	var limit int64
	if err := tx.QueryRow("SELECT links_limit FROM user_subscriptions WHERE user_id=? FOR UPDATE", userID).Scan(&limit); err != nil {
		return false, err
	}
	var used int64
	if err := tx.QueryRow("SELECT COUNT(*) FROM link_Tracker_Link WHERE user_id=?", userID).Scan(&used); err != nil {
		return false, err
	}
	if limit >= 0 && used >= limit {
		return false, nil
	}
	if _, err := tx.Exec("INSERT INTO link_Tracker_Link (user_id,url,slug,urlGenerate) VALUES (?,?,?,?)", userID, target, slug, generatedURL); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// consumeClickQuota is an atomic compare-and-increment, so simultaneous visits
// cannot push an account past its click allowance.
func (e *Env) consumeClickQuota(userID string) (bool, error) {
	if err := e.ensureUserSubscription(userID); err != nil {
		return false, err
	}
	result, err := e.db.Exec(`UPDATE user_subscriptions
		SET clicks_used=clicks_used+1
		WHERE user_id=? AND (expires_at IS NULL OR expires_at > NOW())
		  AND (clicks_limit < 0 OR clicks_used < clicks_limit)`, userID)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	return affected == 1, err
}

func (e *Env) analyticsAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		userID, ok := r.Context().Value(userInfosKey).(string)
		if !ok || userID == "" {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if err := e.ensureUserSubscription(userID); err != nil {
			http.Error(w, `{"error":"subscription_unavailable"}`, http.StatusInternalServerError)
			return
		}
		var allowed bool
		err := e.db.QueryRowContext(r.Context(), `SELECT analytics_enabled FROM user_subscriptions
			WHERE user_id=? AND (expires_at IS NULL OR expires_at > NOW())`, userID).Scan(&allowed)
		if err != nil || !allowed {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"error": "analytics_plan_required",
				"message": "L'analyse est disponible avec les formules à 19 $ et 59 $.",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// checkoutForPlan binds the cart created by the existing checkout handler to the selected plan.
// This prevents a user from paying for a cheaper product and opening another plan's callback.
func (e *Env) checkoutForPlan(plan string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID := cookieRecuperation(w, r, "user_id")
		recorder := httptest.NewRecorder()
		handler(recorder, r)
		if recorder.Code >= http.StatusMultipleChoices && recorder.Code < http.StatusBadRequest {
			for _, cookie := range recorder.Result().Cookies() {
				if cookie.Name != "panier_id" || cookie.Value == "" || userID == "nil" {
					continue
				}
				_, err := e.db.Exec(`INSERT INTO pending_subscription_payments (cart_id, user_id, plan, expires_at)
					VALUES (?, ?, ?, DATE_ADD(NOW(), INTERVAL 2 HOUR))
					ON DUPLICATE KEY UPDATE user_id=VALUES(user_id), plan=VALUES(plan), expires_at=VALUES(expires_at)`,
					cookie.Value, userID, plan)
				if err != nil {
					http.Error(w, "Impossible d'enregistrer la formule du paiement.", http.StatusInternalServerError)
					return
				}
			}
		}
		for key, values := range recorder.Header() {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(recorder.Code)
		_, _ = w.Write(recorder.Body.Bytes())
	}
}
func (e *Env) verifySubscriptionPayment(plan string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		panier := cookieRecuperation(w, r, "panier_id")
		if panier == "nil" {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=unauthorized", http.StatusFound)
			return
		}
		config, ok := subscriptionPlans[plan]
		if !ok || plan == "free" {
			http.Error(w, "Invalid subscription plan", http.StatusBadRequest)
			return
		}

		var pendingUser, pendingPlan string
		if err := e.db.QueryRowContext(r.Context(), `SELECT user_id, plan FROM pending_subscription_payments WHERE cart_id=? AND expires_at > NOW()`, panier).Scan(&pendingUser, &pendingPlan); err != nil || pendingPlan != plan {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=unauthorized", http.StatusFound)
			return
		}
		apiURL := strings.TrimRight(os.Getenv("API_URL"), "/") + "/" + panier
		request, err := http.NewRequestWithContext(r.Context(), http.MethodGet, apiURL, nil)
		if err != nil {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=error", http.StatusFound)
			return
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+os.Getenv("API_KEY"))
		client := &http.Client{Timeout: 10 * time.Second}
		response, err := client.Do(request)
		if err != nil {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=error", http.StatusFound)
			return
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=error", http.StatusFound)
			return
		}
		var payment PaiementStruct
		if err := json.NewDecoder(response.Body).Decode(&payment); err != nil {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=error", http.StatusFound)
			return
		}
		if payment.Statut != "completed" || payment.Meta.UserId == "" {
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=failed", http.StatusFound)
			return
		}

		_, err = e.db.ExecContext(r.Context(), `INSERT INTO user_subscriptions
			(user_id, plan, status, links_limit, clicks_limit, clicks_used, analytics_enabled, period_started_at, expires_at)
			VALUES (?, ?, 'active', ?, ?, 0, ?, NOW(), DATE_ADD(NOW(), INTERVAL 30 DAY))
			ON DUPLICATE KEY UPDATE plan=VALUES(plan), status='active',
			links_limit=VALUES(links_limit), clicks_limit=VALUES(clicks_limit),
			clicks_used=0, analytics_enabled=VALUES(analytics_enabled),
			period_started_at=NOW(), expires_at=VALUES(expires_at)`,
			payment.Meta.UserId, plan, config.LinksLimit, config.ClicksLimit, config.Analytics)
		if err != nil {
			http.Error(w, "Subscription activation failed", http.StatusInternalServerError)
			return
		}
		_, _ = e.db.ExecContext(r.Context(), "UPDATE user_Tracker_Link SET is_active_date=DATE_ADD(NOW(), INTERVAL 30 DAY) WHERE id=?", payment.Meta.UserId)
		http.SetCookie(w, &http.Cookie{Name: "panier_id", Value: "", Path: "/paiement", HttpOnly: true, Secure: true, SameSite: http.SameSiteNoneMode, MaxAge: -1})
		http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?payment=success", http.StatusFound)
	}
}
