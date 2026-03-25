package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"
)

type Id struct{
	Id string `json:"id"`
}

type StructPaiement struct{
	RedirectUrl string `json:"redirectUrl"`
	Cart Id `json:"cart"`
}

func(e *Env) paiement(w http.ResponseWriter, r *http.Request) {
	cookie := cookieRecuperation(w, r, "user_id")
	if cookie == "nil" {
		http.Redirect(w, r,os.Getenv("FRONT") + "/inscription", http.StatusFound)
		return
	}
	
	var (
		name string
		firstName string
		email string
	)

	err := e.db.QueryRow(`SELECT name,first_name,email FROM user_Tracker_Link WHERE id=?`, cookie).Scan(&name,&firstName,&email)
	if err != nil {
		fmt.Println("Erreur récupération utilisateur :", err)
		return
	}

	data := map[string]any{
		"productDocumentId": os.Getenv("ID_PRODUIT"),
		"email":             email,
		"firstName":         firstName,
		"lastName":          name,
		"redirectUrl":       os.Getenv("MY_URL") + "/paiement/verifyPaiement",
		"meta": map[string]any{
			"userId": cookie,
		},
	}

	jsonData, _ := json.Marshal(data)

	req, err := http.NewRequest("POST", os.Getenv("API_URL") + "/checkout", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Fatal(err)
	}

	Authorization := fmt.Sprintf("Bearer %s", os.Getenv("API_KEY"))
	req.Header.Set("Content-type", "application/json")
	req.Header.Set("Authorization", Authorization)

	clent := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := clent.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	var result StructPaiement

	err = json.Unmarshal(body, &result)
	if err != nil {
		log.Fatal("Erreur de decodage JSON: ", err)
	}

	idCookie := &http.Cookie{
		Name: "panier_id",
		Value: result.Cart.Id,
		Path: "/paiement",
		HttpOnly: true,
		Secure: true,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   1200,
	}
	http.SetCookie(w, idCookie)
	http.Redirect(w, r, result.RedirectUrl, http.StatusFound)
}

type Meta struct{
	UserId string `json:"userId"`
}

type PaiementStruct struct{
	Statut string `json:"status"`
	CreatedAt string `json:"created_at"`
	Meta Meta `json:"meta"`
} 

func(e *Env) paiementVerify(w http.ResponseWriter, r *http.Request)  {
	panier := cookieRecuperation(w,r,"panier_id")
	if panier == "nil" {
		http.Redirect(w,r,os.Getenv("FRONT") + "/dashbord?error=Authorize",http.StatusFound)
		return
	}
	url := fmt.Sprintf(os.Getenv("API_URL") + "/%s",panier)
	requete,err := http.NewRequest("GET", url, nil)
	if err != nil {
		fmt.Println("Erreur lors la creation de la requete: ",err)
		return
	}
	Authorization := fmt.Sprintf("Bearer %s", os.Getenv("API_KEY"))
	requete.Header.Set("Content-type", "application/json")
	requete.Header.Set("Authorization", Authorization)

	clent := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := clent.Do(requete)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		log.Fatalf("Response non-OK: %s - %s",resp.Status,body )
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Fatal(err)
	}

	var paiement PaiementStruct

	dateLimite := time.Now().Add(30*24*time.Hour)
	
	err = json.Unmarshal(body,&paiement)
	if err!=nil {
		log.Fatal("Erreur lors du decodage JSON: ",err)
	}

	switch paiement.Statut {
	case "completed":
		_,err := e.db.Exec("UPDATE user_tracker_link SET is_active_date=? WHERE id=?",dateLimite,paiement.Meta.UserId)
		if err!=nil {
			log.Fatal("Erreur lors de la modification de l'utilisateur")
		}
		http.Redirect(w, r,os.Getenv("FRONT") + "/dashboard", http.StatusFound)
		return
	case "payment_failed":
		http.Redirect(w, r,os.Getenv("FRONT") + "/dashbord?error=405", http.StatusFound)
		return
	}

}
