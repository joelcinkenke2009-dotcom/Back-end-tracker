package main

import (
	"crypto/rand"
	"crypto/subtle"
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

	date_limite := time.Now()

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
    SameSite: http.SameSiteLaxMode, // Vous pouvez réutiliser Lax !
    MaxAge:   1500000,
}


	http.SetCookie(w, cookie)
	http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard", http.StatusSeeOther) // Statut 303
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

	err := e.db.QueryRow("SELECT id,password FROM user_Tracker_Link WHERE email=?", email).Scan(&userId, &hash)
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
	http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard", http.StatusSeeOther) // Statut 303
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

	userID, ok := user.(string)
	if !ok || userID == "" {
		http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
		return
	}
	created, err := e.createLinkWithinPlan(userID, url, slug, urlGenerate)
	if err != nil {
		log.Printf("erreur lors de la création du lien pour l'utilisateur %s: %s", userID, err)
		http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?status=500", http.StatusFound)
		return
	}
	if !created {
		http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard?limit=links", http.StatusFound)
		return
	}
	http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard", http.StatusFound)
}

// Mots-clés présents dans le User-Agent des bots, robots d'aperçu de liens,
// outils en ligne de commande et navigateurs automatisés.
var botKeywords = []string{
	// Robots génériques
	"bot", "crawler", "spider", "crawl", "slurp", "archiver", "scanner",
	// Aperçus de liens des réseaux sociaux et messageries
	"facebookexternalhit", "facebot", "meta-externalagent", "meta-externalfetcher",
	// (Telegram, Pinterest et Snapchat ont des navigateurs intégrés à leurs
	// applis : leurs robots sont déjà détectés par "bot" ou "preview".)
	"whatsapp", "skypeuripreview", "embedly", "preview",
	"vkshare", "iframely", "outbrain",
	// Outils et bibliothèques HTTP
	"curl", "wget", "python-requests", "python-urllib", "aiohttp", "httpx",
	"go-http-client", "okhttp", "axios", "node-fetch", "undici", "java/",
	"apache-httpclient", "libwww-perl", "httpclient", "postman", "insomnia",
	// Navigateurs automatisés
	"headlesschrome", "phantomjs", "selenium", "puppeteer", "playwright", "lighthouse",
	// Surveillance de disponibilité
	"pingdom", "uptimerobot", "statuscake", "monitor", "check_http",
}

// isBotRequest indique si la requête provient vraisemblablement d'un bot
// ou d'un préchargement, auquel cas le clic ne doit pas être compté.
func isBotRequest(r *http.Request) bool {
	ua := strings.ToLower(strings.TrimSpace(r.Header.Get("User-Agent")))

	// Un vrai navigateur envoie toujours un User-Agent.
	if ua == "" {
		return true
	}
	for _, keyword := range botKeywords {
		if strings.Contains(ua, keyword) {
			return true
		}
	}

	// Préchargement (Chrome, Safari, Firefox).
	purpose := strings.ToLower(r.Header.Get("Purpose") + " " + r.Header.Get("Sec-Purpose") + " " + r.Header.Get("X-Purpose") + " " + r.Header.Get("X-Moz"))
	if strings.Contains(purpose, "prefetch") || strings.Contains(purpose, "preview") {
		return true
	}

	// Les navigateurs envoient presque toujours un en-tête Accept.
	if r.Header.Get("Accept") == "" {
		return true
	}
	return false
}

// isMobileRequest détecte les téléphones et tablettes.
func isMobileRequest(r *http.Request) bool {
	if r.Header.Get("Sec-CH-UA-Mobile") == "?1" {
		return true
	}
	ua := strings.ToLower(r.Header.Get("User-Agent"))
	for _, k := range []string{"android", "iphone", "ipad", "ipod", "mobile", "blackberry", "opera mini", "iemobile", "windows phone"} {
		if strings.Contains(ua, k) {
			return true
		}
	}
	return false
}

// clientIP récupère l'IP du visiteur, y compris derrière un proxy.
func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Anti-doublon : un même visiteur (IP + User-Agent) n'est compté qu'une fois
// par lien pendant clickDedupWindow.
const clickDedupWindow = 30 * time.Second

var (
	recentClicks   = make(map[string]time.Time)
	recentClicksMu sync.Mutex
)

func init() {
	// Nettoyage périodique pour éviter que la map grossisse indéfiniment.
	go func() {
		for range time.Tick(time.Minute) {
			now := time.Now()
			recentClicksMu.Lock()
			for k, t := range recentClicks {
				if now.Sub(t) > clickDedupWindow {
					delete(recentClicks, k)
				}
			}
			recentClicksMu.Unlock()
		}
	}()
}

// isDuplicateClick renvoie true si ce visiteur a déjà cliqué sur ce lien récemment.
func isDuplicateClick(slug string, r *http.Request) bool {
	key := slug + "|" + clientIP(r) + "|" + r.Header.Get("User-Agent")
	now := time.Now()
	recentClicksMu.Lock()
	defer recentClicksMu.Unlock()
	if t, ok := recentClicks[key]; ok && now.Sub(t) < clickDedupWindow {
		return true
	}
	recentClicks[key] = now
	return false
}

func (e *Env) redirectLink(w http.ResponseWriter, r *http.Request) {
	slug := chi.URLParam(r, "slug")

	var url string
	err := e.db.QueryRow("SELECT url FROM link_Tracker_Link WHERE slug=?", slug).Scan(&url)
	if err != nil {
		log.Printf("Erreur lors de la recuperaton %s du slug=%s", err, slug)
		http.Redirect(w, r, os.Getenv("FRONT"), http.StatusFound)
		return
	}

	// Empêche les navigateurs et proxys de mettre la redirection en cache,
	// sinon les clics suivants ne passeraient plus par le serveur.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")

	// 1. Bots, outils et préchargements : on redirige SANS enregistrer le clic.
	if isBotRequest(r) {
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	// 2. Clics répétés du même visiteur : redirection sans comptage.
	if isDuplicateClick(slug, r) {
		http.Redirect(w, r, url, http.StatusFound)
		return
	}

	// 3. Enregistrement du clic pour les vrais utilisateurs.
	// La disponibilité du site cible est vérifiée à la création du lien
	// (verifyLink) et non plus à chaque clic : un HEAD ici ralentissait chaque
	// visite et bloquait les sites qui refusent la méthode HEAD (405).
	var query string
	if isMobileRequest(r) {
		query = "UPDATE link_Tracker_Link SET click_total=click_total+1, click_mobile=click_mobile+1 WHERE slug=? AND url=?"
	} else {
		query = "UPDATE link_Tracker_Link SET click_total=click_total+1, click_pc=click_pc+1 WHERE slug=? AND url=?"
	}

	_, err = e.db.Exec(query, slug, url)
	if err != nil {
		log.Println("Erreur lors de la modification: ", err)
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

func connexionAdmins(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()

	email := r.FormValue("email")
	password := r.FormValue("password")

	adminEmail := os.Getenv("EMAIL")
	adminPassword := os.Getenv("PASSWORD")

	// Refuse si la configuration admin est absente.
	if adminEmail == "" || adminPassword == "" {
		http.Redirect(w, r, os.Getenv("FRONT")+"/connexion/admins?error=403", http.StatusFound)
		return
	}

	// Comparaison à temps constant pour éviter les attaques temporelles.
	emailOK := subtle.ConstantTimeCompare([]byte(email), []byte(adminEmail)) == 1
	passwordOK := subtle.ConstantTimeCompare([]byte(password), []byte(adminPassword)) == 1

	if !emailOK {
		http.Redirect(w, r, os.Getenv("FRONT")+"/connexion/admins?error=403", http.StatusFound)
		return
	}
	if !passwordOK {
		http.Redirect(w, r, os.Getenv("FRONT")+"/connexion/admins?error=401", http.StatusFound)
		return
	}

	cookie := &http.Cookie{
		Name:     "admin_pass",
		Path:     "/",
		Value:    os.Getenv("COOKIE"),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   12000,
	}
	http.SetCookie(w, cookie)
	http.Redirect(w, r, os.Getenv("FRONT")+"/dashboard/admins", http.StatusFound)
}

func (e *Env) abonnement(w http.ResponseWriter, r *http.Request)  {
	r.ParseForm()

	dateLimite := time.Now().Add(30*24*time.Hour)

	email := r.FormValue("addDate")

	t,err := e.db.Exec("UPDATE user_Tracker_Link SET is_active_date=?  WHERE email=?",dateLimite,email)
	if err!=nil {
		log.Println(err)
		return
	}
	fmt.Println(t.RowsAffected())
	http.Redirect(w,r,os.Getenv("FRONT") + "/dashboard/admins",http.StatusFound)
}
