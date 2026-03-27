package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"golang.org/x/crypto/bcrypt"
)

func hashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func cookieRecuperation(_ http.ResponseWriter, r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil {
		return "nil"
	}
	return cookie.Value
}

func formateUrl(url string) string {
	if !strings.Contains(url, "://") {
		url = "https://" + url
		return url
	}
	return url
}

func (e *Env) inscription(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	name := r.FormValue("name")
	firstName := r.FormValue("firstName")
	fullName := name + " " + firstName
	emailRecive := r.FormValue("email")
	email := strings.ToLower(emailRecive)
	password := r.FormValue("password")
	passwordConfirm := r.FormValue("passwordConfirm")

	date_limite := time.Now().Add(3 * 24 * time.Hour)

	if password != passwordConfirm {
		http.Redirect(w, r,os.Getenv("FRONT") + "/inscription?error=403", http.StatusFound)
		return
	}

	idByte := make([]byte, 32)
	_, err := rand.Read(idByte)
	if err != nil {
		fmt.Println("ERREUR lors de la generation")
		return
	}
	id := hex.EncodeToString(idByte)

	hash, err := hashPassword(password)
	if err != nil {
		fmt.Println("Erreur lors du hashage")
		return
	}

	cookieId := cookieRecuperation(w, r, "user_id")
	if cookieId != "nil" {
		http.Redirect(w, r,os.Getenv("FRONT") + "/inscription?error=402", http.StatusFound)
		return
	}

	_, err = e.db.Exec("INSERT INTO user_Tracker_Link (id,name,first_name,full_name,email,password,is_active_date) VALUES (?,?,?,?,?,?,?)", id, name, firstName, fullName, email, hash, date_limite)
	if err != nil {
		log.Printf("erreur lors de l'insertion %s de l'email=%s", err, email)
		http.Redirect(w, r,os.Getenv("FRONT") + "/inscription?error=401", http.StatusFound)
		return
	}

	cookie := &http.Cookie{
		Name:     "user_id",
		Path:     "/",
		Value:    id,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   1500000,
	}

	http.SetCookie(w, cookie)
	http.Redirect(w, r,os.Getenv("FRONT") + "/dashboard", http.StatusFound)
}

var (
	// Map pour limiter les tentatives
	failedLogin = make(map[string]int)
	mutex       = sync.Mutex{}
	MAX_TRIES   = 5
)

func (e *Env) connexion(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	emailRecive := r.FormValue("email")
	email := strings.ToLower(emailRecive)
	password := r.FormValue("password")

	// Vérifier les tentatives
	mutex.Lock()
	if val, exists := failedLogin[email]; exists && val <= 0 {
		mutex.Unlock()
		http.Redirect(w, r, os.Getenv("FRONT") +"/connexion?error=403", http.StatusFound)
		return
	}
	mutex.Unlock()

	var (
		hash   string
		userId string
	)

	err := e.db.QueryRow("SELECT id,password FROM user_Tracker_link WHERE email=?", email).Scan(&userId, &hash)
	if err != nil {
		log.Printf("Erreur l'email=%s n'existe pas : %s", email, err)
		updateFailedLogin(email)
		http.Redirect(w, r,os.Getenv("FRONT") + "/connexion?error=401", http.StatusFound)
		return
	}

	err = bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	if err != nil {
		log.Printf("l'utilisateur avec l'email=%s n'as pas inscrit le mot de passe correcte : %s", email, err)
		updateFailedLogin(email)
		http.Redirect(w, r,os.Getenv("FRONT") + "/connexion?error=401Unautorized", http.StatusFound)
		return
	}

	// Réinitialiser compteur si succès
	resetFailedLogin(email)

	cookie := &http.Cookie{
		Name:     "user_id",
		Path:     "/",
		Value:    userId,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   1500000,
	}

	http.SetCookie(w, cookie)
	http.Redirect(w, r,os.Getenv("FRONT") + "/dashboard", http.StatusFound)
}

// Décrémente ou initialise le compteur et programme reset après 24h
func updateFailedLogin(email string) {
	mutex.Lock()
	defer mutex.Unlock()
	if _, exists := failedLogin[email]; !exists {
		failedLogin[email] = MAX_TRIES - 1
	} else {
		failedLogin[email]--
	}

	// Reset automatique après 24h
	go func(email string) {
		time.Sleep(24 * time.Hour)
		resetFailedLogin(email)
	}(email)
}

func resetFailedLogin(email string) {
	mutex.Lock()
	defer mutex.Unlock()
	failedLogin[email] = MAX_TRIES
}

// type Request struct {
// 	Url string `json:"urlCreate"`
// }

func (e *Env) createLink(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userInfosKey)
	// var val Request

	// json.NewDecoder(r.Body).Decode(&val)

	url := r.FormValue("url")

	url = formateUrl(strings.ToLower(url))

	idByte := make([]byte, 3)
	_, err := rand.Read(idByte)
	if err != nil {
		fmt.Println("ERREUR lors de la generation")
		return
	}
	slug := hex.EncodeToString(idByte)

	urlGenerate := fmt.Sprintf("%s/link/%s", os.Getenv("MY_URL"), slug)

	_, err = e.db.Exec("INSERT INTO link_Tracker_Link (user_id,url,slug,urlGenerate) VALUES (?,?,?,?)", user, url, slug, urlGenerate)
	if err != nil {
		log.Printf("erreur lors de l'insertion %s de l'id=%s", err, user)
		http.Redirect(w,r,os.Getenv("FRONT")+"/dashboard?status=500",http.StatusFound)
		return
	}
	
	http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard", http.StatusFound)
}

func (e *Env) redirectLink(w http.ResponseWriter, r *http.Request) {

	slug := chi.URLParam(r, "slug")

	var url string

	err := e.db.QueryRow("SELECT url FROM link_Tracker_Link WHERE slug=?", slug).Scan(&url)
	if err != nil {
		log.Printf("Erreur lors de la recuperaton %s du slug=%s", err, slug)
		http.Redirect(w, r,os.Getenv("FRONT") , http.StatusFound)
		return
	}

	res, err := http.Head(url)
	if err != nil || res.StatusCode >= 404 {
		http.Error(w, "Ce site est inaccessible ou invalide", http.StatusBadRequest)
		return
	}
	defer res.Body.Close()

	ua := r.Header.Get("User-Agent")
	isMobile := strings.Contains(ua, "Android") || strings.Contains(strings.ToLower(ua), "iphone")
	if isMobile {
		_, err := e.db.Exec("UPDATE link_Tracker_Link SET click_total=click_total+1,click_mobile=click_mobile+1 WHERE slug=? AND url=?", slug, url)
		if err != nil {
			log.Println("Erreur lors de la modification: ", err)
			return
		}
		http.Redirect(w, r, url, http.StatusFound)
		return
	}
	_,err = e.db.Exec("UPDATE link_Tracker_Link SET click_total=click_total+1,click_pc=click_pc+1 WHERE slug=? AND url=?", slug, url)
	if err != nil {
		log.Println("Erreur lors de la modification: ", err)
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
}

// type SlugDelete struct {
// 	Slug     string `json:"slugDelete"`
// }

func (e *Env) deleteLink(w http.ResponseWriter, r *http.Request) {
	user := r.Context().Value(userInfosKey)

	// var slugDelete SlugDelete

	// json.NewDecoder(r.Body).Decode(&slugDelete)

	slug := r.FormValue("slug")

	_, err := e.db.Exec("DELETE FROM link_Tracker_Link WHERE slug=? AND user_id=?", slug, user)
	if err != nil {
		fmt.Println("Erreur lors de la suppression: ", err)
		return
	}
	http.Redirect(w, r,os.Getenv("FRONT") + "/dashboard", http.StatusFound)	
}

func (e *Env) modifyLink(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	user := r.Context().Value(userInfosKey)

	url := formateUrl(r.FormValue("urlModify"))

	newUrl := formateUrl(r.FormValue("valueModify"))

	_, err := e.db.Exec("UPDATE link_Tracker_Link SET url=? WHERE url=? AND user_id=?", newUrl, url, user)
	if err != nil {
		log.Println("Erreur lors de la modification: ", err)
		http.Redirect(w, r,os.Getenv("FRONT") + "/dashboard?status=500", http.StatusFound)
		return
	}
	http.Redirect(w, r,os.Getenv("FRONT") + "/dashboard", http.StatusFound)
}

// type Url struct {
// 	Url string `json:"link"`
// }

func verifyLink(w http.ResponseWriter, r *http.Request) {
	// var req Url

	// json.NewDecoder(r.Body).Decode(&req)

	req := r.FormValue("link")

	url,err := url.Parse(req)
	if err != nil {
		return
	}

	ip := net.ParseIP(url.Hostname())
	if ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {
		http.Error(w,`{"error":"Forbidden URL"}`, http.StatusForbidden)
		return
	}

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.Head(req)
	if err != nil ||  resp.StatusCode >= 404 {
		if os.IsTimeout(err) {			
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?status=409", http.StatusFound)
			return
		}else{
			http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?status=404", http.StatusFound)
			return
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 403 {
		http.Redirect(w, r, os.Getenv("FRONT")+ "/dashboard?status=200", http.StatusFound)
		return
	}else{
		http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?status=404", http.StatusFound)
		return
	}
}
