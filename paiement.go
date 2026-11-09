package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type Id struct {
	Id string `json:"id"`
}

type StructPaiement struct {
	RedirectUrl string `json:"redirectUrl"`
	Cart        Id     `json:"cart"`
}

type Meta struct {
	UserId string `json:"userId"`
}

type PaiementStruct struct {
	Statut    string `json:"status"`
	CreatedAt string `json:"created_at"`
	Meta      Meta   `json:"meta"`
}

// startCheckout centralizes the three subscription checkout flows. A temporary
// payment-provider or network failure must return an HTTP error, never terminate
// the API process.
func (e *Env) startCheckout(w http.ResponseWriter, r *http.Request, productID, callbackPath string) {
	userID := cookieRecuperation(w, r, "user_id")
	frontURL := strings.TrimRight(os.Getenv("FRONT"), "/")
	if userID == "nil" || userID == "" {
		http.Redirect(w, r, frontURL+"/inscription", http.StatusFound)
		return
	}

	if productID == "" || os.Getenv("API_URL") == "" || os.Getenv("API_KEY") == "" || os.Getenv("MY_URL") == "" {
		log.Printf("payment checkout is not configured: user_id=%q", userID)
		http.Error(w, "Le paiement est temporairement indisponible. Réessayez plus tard.", http.StatusServiceUnavailable)
		return
	}

	var name, firstName, email string
	if err := e.db.QueryRowContext(r.Context(),
		"SELECT name, first_name, email FROM user_Tracker_Link WHERE id=?", userID,
	).Scan(&name, &firstName, &email); err != nil {
		log.Printf("payment user lookup failed: user_id=%q: %v", userID, err)
		http.Error(w, "Impossible de préparer votre paiement. Réessayez.", http.StatusInternalServerError)
		return
	}

	payload := map[string]any{
		"productDocumentId": productID,
		"email":             email,
		"firstName":         firstName,
		"lastName":          name,
		"redirectUrl":       strings.TrimRight(os.Getenv("MY_URL"), "/") + callbackPath,
		"meta": map[string]any{
			"userId": userID,
		},
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Printf("payment payload encoding failed: user_id=%q: %v", userID, err)
		http.Error(w, "Impossible de préparer votre paiement.", http.StatusInternalServerError)
		return
	}

	endpoint := strings.TrimRight(os.Getenv("API_URL"), "/") + "/checkout"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint, bytes.NewReader(jsonData))
	if err != nil {
		log.Printf("payment request creation failed: user_id=%q: %v", userID, err)
		http.Error(w, "Le service de paiement est temporairement indisponible.", http.StatusBadGateway)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+os.Getenv("API_KEY"))

	client := &http.Client{Timeout: 25 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("payment provider request failed: user_id=%q: %v", userID, err)
		http.Error(w, "Le service de paiement ne répond pas. Réessayez dans quelques instants.", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		log.Printf("payment provider returned HTTP %d: user_id=%q", resp.StatusCode, userID)
		http.Error(w, "Le fournisseur de paiement n'a pas pu créer votre commande.", http.StatusBadGateway)
		return
	}

	var result StructPaiement
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		log.Printf("payment provider response decoding failed: user_id=%q: %v", userID, err)
		http.Error(w, "Réponse invalide du fournisseur de paiement. Réessayez.", http.StatusBadGateway)
		return
	}
	if result.Cart.Id == "" {
		log.Printf("payment provider response has no cart id: user_id=%q", userID)
		http.Error(w, "La commande de paiement n'a pas pu être créée.", http.StatusBadGateway)
		return
	}

	checkoutURL, err := url.Parse(result.RedirectUrl)
	if err != nil || !checkoutURL.IsAbs() || checkoutURL.Scheme != "https" || checkoutURL.Host == "" {
		log.Printf("payment provider returned an invalid checkout URL: user_id=%q", userID)
		http.Error(w, "L'adresse sécurisée du paiement est invalide. Contactez le support.", http.StatusBadGateway)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "panier_id",
		Value:    result.Cart.Id,
		Path:     "/paiement",
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   1200,
	})
	http.Redirect(w, r, checkoutURL.String(), http.StatusFound)
}

func (e *Env) paiement(w http.ResponseWriter, r *http.Request) {
	e.startCheckout(w, r, os.Getenv("ID_PRODUIT"), "/paiement/verifyPaiement")
}

func (e *Env) paiement2(w http.ResponseWriter, r *http.Request) {
	e.startCheckout(w, r, "87b79ab6-ad20-4d6d-b219-6d8ab831ef6e", "/paiement/verifyPaiement2")
}

func (e *Env) paiement3(w http.ResponseWriter, r *http.Request) {
	e.startCheckout(w, r, "c8dec53d-aac0-4d40-bd94-0574dcf99cbf", "/paiement/verifyPaiement3")
}
