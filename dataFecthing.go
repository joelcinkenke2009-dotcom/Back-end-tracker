package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"
)

type UserInfos struct {
	Link        string `db:"url" json:"url"`
	Slug        string `db:"slug" json:"slug"`
	UrlGenerate string `db:"urlGenerate" json:"urlGenerate"`
	ClickTotal  int    `db:"click_total" json:"click_total"`
	ClickPC     int    `db:"click_pc" json:"click_pc"`
	ClickMobile int    `db:"click_mobile" json:"click_mobile"`
}

func setJSONHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}

func parsePagination(r *http.Request) (limit, offset int) {
	const (
		defaultLimit = 500
		maxLimit     = 1000
	)
	limit = defaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			limit = parsed
		}
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	offset = 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
			offset = parsed
		}
	}
	return limit, offset
}

func (e *Env) dataOnly(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")

	// AuthMiddleware has already validated the session and placed the user ID
	// in the request context. Avoid parsing the cookie a second time.
	user, ok := r.Context().Value(userInfosKey).(string)
	if !ok || user == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}

	limit, offset := parsePagination(r)
	// Fetch one extra row to tell the frontend whether another page exists.
	rows, err := e.db.QueryContext(r.Context(), `
		SELECT url, slug, urlGenerate, click_total, click_mobile, click_pc
		FROM link_Tracker_Link
		WHERE user_id = ?
		ORDER BY click_total DESC, id DESC
		LIMIT ? OFFSET ?`, user, limit+1, offset)
	if err != nil {
		fmt.Println("Erreur récupération des liens :", err)
		http.Error(w, `{"error":"data_unavailable"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	links := make([]UserInfos, 0, limit)
	hasMore := false
	for rows.Next() {
		var item UserInfos
		if err := rows.Scan(&item.Link, &item.Slug, &item.UrlGenerate, &item.ClickTotal, &item.ClickMobile, &item.ClickPC); err != nil {
			fmt.Println("Erreur scan des liens :", err)
			http.Error(w, `{"error":"data_unavailable"}`, http.StatusInternalServerError)
			return
		}
		if len(links) == limit {
			hasMore = true
			break
		}
		links = append(links, item)
	}
	if err := rows.Err(); err != nil {
		fmt.Println("Erreur parcours des liens :", err)
		http.Error(w, `{"error":"data_unavailable"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("X-Has-More", strconv.FormatBool(hasMore))
	w.Header().Set("X-Limit", strconv.Itoa(limit))
	w.Header().Set("X-Offset", strconv.Itoa(offset))
	if hasMore {
		w.Header().Set("Link", fmt.Sprintf(`<?limit=%d&offset=%d>; rel="next"`, limit, offset+limit))
	}

	// Keep the existing JSON array response so current Vue components remain
	// compatible. Pagination is opt-in from the frontend via limit/offset.
	if err := json.NewEncoder(w).Encode(links); err != nil {
		fmt.Println("Erreur envoi des liens :", err)
	}
}

func isConnected(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))

	cookie := cookieRecuperation(w, r, "user_id")
	if cookie == "nil" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"login": "Veuillez vous inscrire ou vous connecter pour continuer",
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"connected": "Vous etes connecté",
	})
}

func (e *Env) isActive(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))

	cookie := cookieRecuperation(w, r, "user_id")
	if cookie == "nil" {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"login": "Veuillez vous inscrire ou vous connecter pour continuer",
		})
		return
	}

	var activeUntil time.Time
	err := e.db.QueryRowContext(r.Context(), "SELECT is_active_date FROM user_Tracker_Link WHERE id=?", cookie).Scan(&activeUntil)
	if err != nil {
		fmt.Println("Erreur récupération utilisateur :", err)
		http.Error(w, `{"error":"user_unavailable"}`, http.StatusInternalServerError)
		return
	}
	formatted := activeUntil.Local().Format("02 January 2006 à 15:04")
	if time.Now().Before(activeUntil) {
		_ = json.NewEncoder(w).Encode(map[string]any{"date_limite": formatted})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"expire": formatted})
}

type AdminData struct {
	FullName string `db:"full_name" json:"full_name"`
	Email    string `db:"email" json:"email"`
	Id       string `db:"id" json:"id"`
	IsActive string `db:"is_active_date" json:"is_active_date"`
}

func AdminsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		setJSONHeaders(w)
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))

		cookie := cookieRecuperation(w, r, "admin_pass")
		expected := os.Getenv("COOKIE")
		if expected != "" && subtle.ConstantTimeCompare([]byte(cookie), []byte(expected)) == 1 {
			next.ServeHTTP(w, r)
			return
		}
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"Acces": "NO"})
	})
}

func (e *Env) dataAdmins(w http.ResponseWriter, r *http.Request) {
	setJSONHeaders(w)
	w.Header().Set("Cache-Control", "private, no-store")

	rows, err := e.db.QueryContext(r.Context(), "SELECT id, full_name, email, is_active_date FROM user_Tracker_Link")
	if err != nil {
		fmt.Printf("Erreur lors de la récupération des données : %s", err)
		http.Error(w, `{"error":"data_unavailable"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	allUsers := make([]AdminData, 0)
	for rows.Next() {
		var user AdminData
		if err := rows.Scan(&user.Id, &user.FullName, &user.Email, &user.IsActive); err != nil {
			fmt.Printf("Erreur lors de la lecture des utilisateurs : %s", err)
			http.Error(w, `{"error":"data_unavailable"}`, http.StatusInternalServerError)
			return
		}
		allUsers = append(allUsers, user)
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("Erreur parcours des utilisateurs : %s", err)
		http.Error(w, `{"error":"data_unavailable"}`, http.StatusInternalServerError)
		return
	}
	_ = json.NewEncoder(w).Encode(allUsers)
}
