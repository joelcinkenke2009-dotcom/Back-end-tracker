package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/mail"
	"os"
	"strconv"
	"strings"
	"time"
)

type googleTokenInfo struct {
	Audience      string `json:"aud"`
	Issuer        string `json:"iss"`
	Subject       string `json:"sub"`
	Email         string `json:"email"`
	EmailVerified string `json:"email_verified"`
	ExpiresAt     string `json:"exp"`
	Name          string `json:"name"`
	GivenName     string `json:"given_name"`
	FamilyName    string `json:"family_name"`
}

type googleAuthRequest struct {
	Credential string `json:"credential"`
}

func (e *Env) googleAuth(w http.ResponseWriter, r *http.Request) {
	origin := strings.TrimRight(os.Getenv("FRONT"), "/")
	if origin == "" {
		http.Error(w, `{"error":"server_configuration"}`, http.StatusInternalServerError)
		return
	}
	if requestOrigin := r.Header.Get("Origin"); requestOrigin != "" && requestOrigin == origin {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Vary", "Origin")
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodOptions {
		if r.Header.Get("Origin") != origin {
			http.Error(w, `{"error":"origin_not_allowed"}`, http.StatusForbidden)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Origin") != origin {
		http.Error(w, `{"error":"origin_not_allowed"}`, http.StatusForbidden)
		return
	}
	if os.Getenv("GOOGLE_CLIENT_ID") == "" {
		http.Error(w, `{"error":"google_not_configured"}`, http.StatusServiceUnavailable)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 16*1024)
	var request googleAuthRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil || strings.TrimSpace(request.Credential) == "" {
		http.Error(w, `{"error":"invalid_google_credential"}`, http.StatusBadRequest)
		return
	}

	claims, err := verifyGoogleIDToken(r.Context(), request.Credential, os.Getenv("GOOGLE_CLIENT_ID"))
	if err != nil {
		log.Printf("Google sign-in rejected: %v", err)
		http.Error(w, `{"error":"google_verification_failed"}`, http.StatusUnauthorized)
		return
	}

	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if _, err := mail.ParseAddress(email); err != nil || claims.Subject == "" {
		http.Error(w, `{"error":"invalid_google_profile"}`, http.StatusUnauthorized)
		return
	}

	var userID string
	err = e.db.QueryRowContext(r.Context(), "SELECT id FROM user_Tracker_Link WHERE email=? LIMIT 1", email).Scan(&userID)
	if err != nil && err.Error() != "sql: no rows in result set" {
		log.Printf("Google account lookup failed: %v", err)
		http.Error(w, `{"error":"account_lookup_failed"}`, http.StatusInternalServerError)
		return
	}
	if userID == "" {
		idBytes := make([]byte, 32)
		if _, err := rand.Read(idBytes); err != nil {
			http.Error(w, `{"error":"account_creation_failed"}`, http.StatusInternalServerError)
			return
		}
		userID = hex.EncodeToString(idBytes)

		givenName := strings.TrimSpace(claims.GivenName)
		familyName := strings.TrimSpace(claims.FamilyName)
		if givenName == "" && familyName == "" {
			parts := strings.Fields(claims.Name)
			if len(parts) > 0 {
				givenName = parts[0]
			}
			if len(parts) > 1 {
				familyName = strings.Join(parts[1:], " ")
			}
		}
		fullName := strings.TrimSpace(strings.TrimSpace(familyName) + " " + strings.TrimSpace(givenName))
		if fullName == "" {
			fullName = email
		}
		if _, err := e.db.ExecContext(r.Context(), `INSERT INTO user_Tracker_Link
			(id,name,first_name,full_name,email,password,is_active_date,status)
			VALUES (?,?,?,?,?,'',?,1)`, userID, familyName, givenName, fullName, email, time.Now()); err != nil {
			// Another simultaneous Google callback may have created this email.
			if lookupErr := e.db.QueryRowContext(r.Context(), "SELECT id FROM user_Tracker_Link WHERE email=? LIMIT 1", email).Scan(&userID); lookupErr != nil {
				log.Printf("Google account creation failed: %v", err)
				http.Error(w, `{"error":"account_creation_failed"}`, http.StatusInternalServerError)
				return
			}
		}
	}

	if err := e.ensureUserSubscription(userID); err != nil {
		log.Printf("Google subscription initialization failed for %s: %v", userID, err)
		http.Error(w, `{"error":"subscription_initialization_failed"}`, http.StatusInternalServerError)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name: "user_id", Value: userID, Path: "/", HttpOnly: true,
		Secure: true, SameSite: http.SameSiteNoneMode, MaxAge: 1500000,
	})
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func verifyGoogleIDToken(parent context.Context, idToken, clientID string) (*googleTokenInfo, error) {
	ctx, cancel := context.WithTimeout(parent, 5*time.Second)
	defer cancel()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://oauth2.googleapis.com/tokeninfo?id_token="+idToken, nil)
	if err != nil {
		return nil, err
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Google tokeninfo returned %s", response.Status)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, 32*1024))
	if err != nil {
		return nil, err
	}
	var claims googleTokenInfo
	if err := json.Unmarshal(body, &claims); err != nil {
		return nil, err
	}
	if claims.Audience != clientID {
		return nil, fmt.Errorf("token audience mismatch")
	}
	if claims.Issuer != "accounts.google.com" && claims.Issuer != "https://accounts.google.com" {
		return nil, fmt.Errorf("invalid token issuer")
	}
	if claims.Subject == "" || claims.Email == "" || claims.EmailVerified != "true" {
		return nil, fmt.Errorf("Google email is not verified")
	}
	expiry, err := strconv.ParseInt(claims.ExpiresAt, 10, 64)
	if err != nil || time.Now().Unix() >= expiry {
		return nil, fmt.Errorf("Google token expired")
	}
	return &claims, nil
}
