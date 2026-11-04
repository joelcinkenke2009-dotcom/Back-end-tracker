package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

type contextKey string

const userInfosKey contextKey = "userInfos"

// --------------------
// Middleware
// --------------------
func(e *Env) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))

		userID := cookieRecuperation(w, r, "user_id")
		if userID == "nil" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"login": "Veuillez vous inscrire ou vous connecter pour continuer",
			})
			return
		}
		var exists int
		if err := e.db.QueryRowContext(r.Context(), "SELECT COUNT(*) FROM user_Tracker_Link WHERE id=?", userID).Scan(&exists); err != nil || exists == 0 {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"login": "Veuillez vous inscrire ou vous connecter pour continuer",
			})
			return
		}
		if err := e.ensureUserSubscription(userID); err != nil {
			http.Error(w, `{"error":"subscription_unavailable"}`, http.StatusInternalServerError)
			return
		}
		ctx := context.WithValue(r.Context(), userInfosKey, userID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func(e *Env) AuthMetaMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Headers
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))


		cookie := cookieRecuperation(w, r, "user_id")
		if cookie == "nil" {
			json.NewEncoder(w).Encode(map[string]any{
				"login": "Veuillez vous inscrire ou vous connecter pour continuer",
			})
			return
		}

		var date time.Time

		err := e.db.QueryRow(`SELECT is_active_date FROM user_Tracker_Link WHERE id=?`, cookie).Scan(&date)
		if err != nil {
			fmt.Println("Erreur récupération utilisateur :", err)
			json.NewEncoder(w).Encode(map[string]any{
				"login": "Veuillez vous inscrire ou vous connecter pour continuer",
			})
			return
		}

		if time.Now().Before(date) {
			var accesToken string
			err := e.db.QueryRow(`SELECT access_token FROM user_meta_ad_connect WHERE user_id=?`,cookie).Scan(&accesToken)
			if err != nil {
				fmt.Println("Erreur récupération utilisateur :", err)
				json.NewEncoder(w).Encode(map[string]any{
					"login": "Veuillez vous inscrire ou vous connecter pour continuer",
				})
				return
			}
			if accesToken != "" {
				ctx := context.WithValue(r.Context(), userInfosKey, cookie)
				r = r.WithContext(ctx)
		
				next.ServeHTTP(w, r)
				return
			}
		}

		json.NewEncoder(w).Encode(map[string]any{
			"Paiement": "Abonnement expiré. Merci de le renouveler pour contunuer a utiliser nos services",
		})
	})
}
