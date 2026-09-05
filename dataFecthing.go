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


var date time.Time

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

	err := e.db.QueryRow(`SELECT is_active_date FROM user_Tracker_Link WHERE id=?`, cookie).Scan(&date)
	if err != nil {
		fmt.Println("Erreur récupération utilisateur :", err)
		return
	}
	formate := date.Local().Format("02 January 2006 à 15:04 ")
	if time.Now().Before(date) {
		json.NewEncoder(w).Encode(map[string]any{
			"date_limite":formate,
		})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{
		"expire":formate,
	})
}

type AdminData struct{
	FullName string `db:"full_name"`
	Email string `db:"email"`
	Id string `db:"id"`
	IsActive string `db:"is_active_date"`
}



func AdminsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Methods", "GET")
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Origin", os.Getenv("FRONT"))
	
		cookie := cookieRecuperation(w,r,"admin_pass")

		if cookie == os.Getenv("COOKIE") {
			next.ServeHTTP(w,r)
			return 
		} else {
			json.NewEncoder(w).Encode(map[string]any{
				"Acces":"NO",
			})
			http.Redirect(w,r,os.Getenv("FRONT") + "/dashbord/admins?err=401",http.StatusNotFound)		
		}
	})
}


func (e *Env) dataAdmins(w http.ResponseWriter, r *http.Request) {
	
	var admins AdminData 

	data,err := e.db.Query("SELECT id,full_name,email,is_active_date FROM user_Tracker_Link")	
	if err != nil {
		fmt.Printf("Erreur lors de la recuperation des données %s",err)
		return
	}
	var allUsers []AdminData
	for data.Next() {
		err := data.Scan(&admins.Id,&admins.FullName,&admins.Email,&admins.IsActive)
		if err != nil {
			fmt.Printf("Erreur lors de l'insertion %s",err)
			return
		}
		allUsers = append(allUsers, admins)
	}
	json.NewEncoder(w).Encode(allUsers)
}
