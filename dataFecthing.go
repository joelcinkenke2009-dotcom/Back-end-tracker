package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

type UserInfos struct {
	Link string `db:"url"`
	Slug string `db:"slug"`
	UrlGenerate string `db:"urlGenerate"`
	ClickTotal int `db:"click_total"`
	ClickPC int `db:"click_pc"`
	ClickMobile int `db:"click_mobile"`
}


func(e *Env) dataOnly(w http.ResponseWriter, r *http.Request) {
	user := cookieRecuperation(w, r, "user_id")
	if user == "nil" {
		return
	}
	data, err := e.db.Query(`SELECT url,slug,urlGenerate,click_total,click_mobile,click_pc FROM link_Tracker_Link WHERE user_id=? ORDER BY click_total DESC`, user)
	if err != nil {
		fmt.Println("Erreur récupération utilisateur :", err)
		return
	}
	defer data.Close()

	var utilisateurs []UserInfos

	for data.Next() {
		var value UserInfos
		err := data.Scan(&value.Link, &value.Slug, &value.UrlGenerate, &value.ClickTotal, &value.ClickMobile, &value.ClickPC)
		if err != nil {
			json.NewEncoder(w).Encode(map[string]any{
				"link": "Veuillez ajouter un lien",
			})
			fmt.Println("Erreur scan :", err)
			return
		}
		utilisateurs = append(utilisateurs, value)
	}

	date := time.Now().Add(24*7*time.Hour)
	cookie := &http.Cookie{
		Name:     "user_id",
		Path:     "/",
		Value:    user,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		Expires:   date,
	}
	http.SetCookie(w,cookie)
	json.NewEncoder(w).Encode(utilisateurs)
}



func isConnected(w http.ResponseWriter, r *http.Request)  {
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
	json.NewEncoder(w).Encode(map[string]any{
		"connected": "Vous etes connecté",
	})
}

func(e *Env) isActive(w http.ResponseWriter, r *http.Request)  {
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
		return
	}
	if time.Now().Before(date) {
		formate := date.Local().Format("02 January 2006 à 15:04 ")
		json.NewEncoder(w).Encode(map[string]any{
			"date_limite":formate,
		})
		return
	}
	formate := date.Local().Format("02 January 2006 à 15:04 ")
	json.NewEncoder(w).Encode(map[string]any{
		"expire":formate,
	})
}