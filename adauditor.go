package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
}

func (e *Env) HandleMetaCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	if code == "" {
		http.Error(w, "Code manquant", http.StatusBadRequest)
		return
	}

	appID := os.Getenv("META_APP_ID")
	appSecret := os.Getenv("META_APP_SECRET")
	redirectURI := os.Getenv("META_REDIRECT_URI")

	// 1. Échange du code contre un Short-Lived Access Token
	urlShort := fmt.Sprintf(
		"https://graph.facebook.com/v19.0/oauth/access_token?client_id=%s&redirect_uri=%s&client_secret=%s&code=%s",
		appID, redirectURI, appSecret, code,
	)

	resp, err := http.Get(urlShort)
	if err != nil {
		http.Error(w, "Erreur lors de la requête OAuth", http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Erreur Meta OAuth (Short Token): status %d", resp.StatusCode)
		http.Error(w, "Échec de l'authentification Meta "err, http.StatusBadRequest)
		return
	}

	var shortTokenRes TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&shortTokenRes); err != nil || shortTokenRes.AccessToken == "" {
		log.Printf("Erreur décodage Short Token: %v", err)
		http.Error(w, "Impossible de lire le token court", http.StatusInternalServerError)
		return
	}

	// 2. Conversion en Long-Lived Token (valable 60 jours)
	urlLong := fmt.Sprintf(
		"https://graph.facebook.com/v19.0/oauth/access_token?grant_type=fb_exchange_token&client_id=%s&client_secret=%s&fb_exchange_token=%s",
		appID, appSecret, shortTokenRes.AccessToken,
	)

	respLong, err := http.Get(urlLong)
	if err != nil {
		http.Error(w, "Erreur extension de token", http.StatusInternalServerError)
		return
	}
	defer respLong.Body.Close()

	if respLong.StatusCode != http.StatusOK {
		log.Printf("Erreur Meta OAuth (Long Token): status %d", respLong.StatusCode)
		http.Error(w, "Échec d'extension du token", http.StatusBadRequest)
		return
	}

	var longTokenRes TokenResponse
	if err := json.NewDecoder(respLong.Body).Decode(&longTokenRes); err != nil || longTokenRes.AccessToken == "" {
		log.Printf("Erreur décodage Long Token: %v", err)
		http.Error(w, "Impossible de lire le token final", http.StatusInternalServerError)
		return
	}

	user := cookieRecuperation(w, r, "user_id")
	if user == "nil" {
		http.Error(w, "Utilisateur non authentifié", http.StatusUnauthorized)
		return
	}

	expire := time.Now().Add(time.Duration(longTokenRes.ExpiresIn) * time.Second)

	// 3. Sauvegarde ou mise à jour du token en base de données (compatible PostgreSQL)
	query := `
		INSERT INTO user_meta_ad (user_id, access_token, expires_at) VALUES (?, ?, ?)`
	_, err = e.db.Exec(query, user, longTokenRes.AccessToken, expire)
	if err != nil {
		log.Printf("Erreur lors de l'insertion DB: %v", err)
		http.Redirect(w, r, os.Getenv("FRONT")+"/inscription?error=401", http.StatusFound)
		return
	}

	// Rediriger l'utilisateur vers le dashboard Vue.js
	http.Redirect(w, r, os.Getenv("FRONT")+"/dashboardADS?meta_connected=true", http.StatusSeeOther)
}

func (e *Env) metaResponse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Methods", "GET")
	w.Header().Set("Access-Control-Allow-Credentials", "true")
	w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))

	user := cookieRecuperation(w, r, "user_id")
	if user == "nil" || user == "" {
		http.Error(w, "Non autorisé", http.StatusUnauthorized)
		return
	}

	var accessToken string
	err := e.db.QueryRow(`SELECT access_token FROM user_meta_ad WHERE user_id=?`, user).Scan(&accessToken)
	if err != nil {
		fmt.Println("Erreur récupération utilisateur :", err)
		http.Error(w, "Utilisateur introuvable", http.StatusUnauthorized)
		return
	}

	type AdAccount struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	type AccountResponse struct {
		Data []AdAccount `json:"data"`
	}

	type InsightData struct {
		Spend       string `json:"spend"`
		Impressions string `json:"impressions"`
		Clicks      string `json:"clicks"`
		Cpc         string `json:"cpc"`
		Ctr         string `json:"ctr"`
	}
	type InsightsResponse struct {
		Data []InsightData `json:"data"`
	}

	type AdItem struct {
		ID       string           `json:"id"`
		Name     string           `json:"name"`
		Status   string           `json:"status"`
		Insights InsightsResponse `json:"insights"`
	}
	type AdsResponse struct {
		Data []AdItem `json:"data"`
	}

	type SingleAdResult struct {
		AdID        string `json:"ad_id"`
		AdName      string `json:"ad_name"`
		Status      string `json:"status"`
		Spend       string `json:"spend"`
		Impressions string `json:"impressions"`
		Clicks      string `json:"clicks"`
		Cpc         string `json:"cpc"`
		Ctr         string `json:"ctr"`
	}

	// 1. Récupération du compte publicitaire
	urlAccount := fmt.Sprintf("https://graph.facebook.com/v19.0/me/adaccounts?fields=id,name&access_token=%s", accessToken)
	respAcc, err := http.Get(urlAccount)
	if err != nil || respAcc.StatusCode != http.StatusOK {
		http.Error(w, "Erreur récupération compte publicitaire", http.StatusInternalServerError)
		return
	}
	defer respAcc.Body.Close()

	var accRes AccountResponse
	if err := json.NewDecoder(respAcc.Body).Decode(&accRes); err != nil || len(accRes.Data) == 0 {
		http.Error(w, "Aucun compte publicitaire trouvé", http.StatusNotFound)
		return
	}

	adAccountID := accRes.Data[0].ID

	// 2. Récupération des publicités avec limite à 100
	fields := "id,name,status,insights.date_preset(last_30d){spend,impressions,clicks,cpc,ctr}"
	urlAds := fmt.Sprintf(
		"https://graph.facebook.com/v19.0/%s/ads?fields=%s&limit=100&access_token=%s",
		adAccountID, fields, accessToken,
	)

	respAds, err := http.Get(urlAds)
	if err != nil || respAds.StatusCode != http.StatusOK {
		http.Error(w, "Erreur récupération des publicités", http.StatusInternalServerError)
		return
	}
	defer respAds.Body.Close()

	var adsRes AdsResponse
	json.NewDecoder(respAds.Body).Decode(&adsRes)

	// Initalisation explicite d'un tableau vide pour éviter de renvoyer "null"
	adList := make([]SingleAdResult, 0)

	for _, ad := range adsRes.Data {
		item := SingleAdResult{
			AdID:   ad.ID,
			AdName: ad.Name,
			Status: ad.Status,
			Spend:  "0", Impressions: "0", Clicks: "0", Cpc: "0", Ctr: "0",
		}

		if len(ad.Insights.Data) > 0 {
			insight := ad.Insights.Data[0]
			item.Spend = insight.Spend
			item.Impressions = insight.Impressions
			item.Clicks = insight.Clicks
			item.Cpc = insight.Cpc
			item.Ctr = insight.Ctr
		}

		adList = append(adList, item)
	}



	json.NewEncoder(w).Encode(adList)
}

