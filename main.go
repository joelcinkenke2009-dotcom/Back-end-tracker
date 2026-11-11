package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/go-sql-driver/mysql"
)

type Env struct {
	db    *sql.DB
	cache *MemoryCache
}


func(e *Env) createTables(){

	createTable := `CREATE TABLE IF NOT EXISTS user_Tracker_Link (
		id VARCHAR(255),
		name VARCHAR(250),
		first_name VARCHAR(250),
		full_name VARCHAR(250),
		password VARCHAR(255),
		email VARCHAR(250) UNIQUE NOT NULL ,
		is_active_date TIMESTAMP,
		status TINYINT NOT NULL DEFAULT 1,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (email)
	);`
	_, err :=  e.db.Exec(createTable)
	if err != nil {
		panic(err)
	}

	// User IDs are used by authentication and account lookups.
	var userIDIndexExists int
	err = e.db.QueryRow("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'user_Tracker_Link' AND index_name = 'idx_user_tracker_id'").Scan(&userIDIndexExists)
	if err != nil {
		panic(err)
	}
	if userIDIndexExists == 0 {
		if _, err = e.db.Exec("CREATE INDEX idx_user_tracker_id ON user_Tracker_Link (id)"); err != nil {
			panic(err)
		}
	}

	createTableLink := `CREATE TABLE IF NOT EXISTS link_Tracker_Link (
		id INT AUTO_INCREMENT PRIMARY KEY,
		user_id VARCHAR(250),
		url VARCHAR(250),
		slug VARCHAR(250),
		urlGenerate VARCHAR(250),
		click_total INT DEFAULT 0,
		click_mobile INT DEFAULT 0,
		click_pc INT DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err = e.db.Exec(createTableLink)
	if err != nil {
		panic(err)
	}

	// Index used by the dashboard query: WHERE user_id=? ORDER BY click_total.
	var indexExists int
	err = e.db.QueryRow("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'link_Tracker_Link' AND index_name = 'idx_link_user_clicks'").Scan(&indexExists)
	if err != nil {
		panic(err)
	}
	if indexExists == 0 {
		if _, err = e.db.Exec("CREATE INDEX idx_link_user_clicks ON link_Tracker_Link (user_id, click_total, id)"); err != nil {
			panic(err)
		}
	}

	// Redirects and link updates frequently resolve a row by its slug.
	var slugIndexExists int
	err = e.db.QueryRow("SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'link_Tracker_Link' AND index_name = 'idx_link_slug'").Scan(&slugIndexExists)
	if err != nil {
		panic(err)
	}
	if slugIndexExists == 0 {
		if _, err = e.db.Exec("CREATE INDEX idx_link_slug ON link_Tracker_Link (slug)"); err != nil {
			panic(err)
		}
	}

	createTableMeta := `CREATE TABLE IF NOT EXISTS user_meta_ad_connect (
    id INT AUTO_INCREMENT PRIMARY KEY,
    user_id VARCHAR(255) UNIQUE NOT NULL,
    access_token TEXT NOT NULL,
    expires_at TIMESTAMP NOT NULL,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`
	_, err =  e.db.Exec(createTableMeta)
	if err != nil {
		panic(err)
	}
}

func main() {
	
	// if os.Getenv("ENVIRONNEMENT") == "" {
	// 	err := godotenv.Load()
	// 	if err != nil {
	// 		fmt.Println("Erreur lors de l'accés au variable d'evironnement")
	// 		return
	// 	}	
	// }
	r := chi.NewRouter()
	// Compress JSON responses to reduce dashboard transfer size on slower networks.
	r.Use(middleware.Compress(5, "application/json"))

	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/%s?parseTime=true&tls=skip-verify",os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD"), os.Getenv("DB_HOST"), os.Getenv("DB_PORT"), os.Getenv("DB_DATABASE"))
	sqlDB, err := sql.Open("mysql", dsn)
	if err != nil {
		log.Panic("Erreur lors de la connexion MySQL :", err)
		return
	}
	if err=sqlDB.Ping();err!=nil {
		log.Println("Impossible de me connecter: ",err)
		return
	}
	sqlDB.SetMaxOpenConns(25)
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetConnMaxIdleTime(30 * time.Second)
	sqlDB.SetConnMaxLifetime(5 * time.Minute)
	
	defer sqlDB.Close()

	app := &Env{db: sqlDB, cache: NewMemoryCache(10000)}

	app.createTables()
	if err := app.ensureSubscriptionTables(); err != nil {
		log.Panic("Erreur création des tables abonnements :", err)
	}
	if err := app.ensureAnalyticsTables(); err != nil {
		log.Panic("Erreur création des tables analytics :", err)
	}
	app.startClickDedupCleanup()

	r.Post("/users/inscription", app.inscription)
	r.Post("/users/connexion", app.connexion)
	r.HandleFunc("/users/google", app.googleAuth)
	r.Post("/admins/connexion", connexionAdmins)
	r.Post("/paiement/initialisation", app.checkoutForPlan("standard", app.paiement))
	r.Post("/paiement/initialisation2", app.checkoutForPlan("unlimited", app.paiement2))
	r.Post("/paiement/initialisation3", app.checkoutForPlan("advanced", app.paiement3))
	r.With(AdminsMiddleware).Post("/abonnement", app.abonnement)
	r.Get("/paiement/verifyPaiement", app.verifySubscriptionPayment("standard"))
	r.Get("/paiement/verifyPaiement2", app.verifySubscriptionPayment("unlimited"))
	r.Get("/paiement/verifyPaiement3", app.verifySubscriptionPayment("advanced"))
	r.Get("/ISCONNECTED", isConnected)
	r.Get("/isActive",app.subscriptionStatus)
    r.Get("/api/auth/facebook/callback", app.HandleMetaCallback)
	r.With(app.AuthMetaMiddleware).Get("/api/facebook/response",app.metaResponse)
	r.With(AdminsMiddleware).Get("/admins",app.dataAdmins)
	r.Route("/protected", func(r chi.Router) {
		r.Use(app.AuthMiddleware)
		r.Get("/data", app.dataOnly)
		r.Post("/delete",app.deleteLink)
		r.Post("/createLink", app.createLink)
		r.Post("/modify", app.modifyLink)
		r.Post("/verifyLink", verifyLink)
		r.With(app.analyticsAccessMiddleware, app.cacheAnalytics(10*time.Second, "summary")).Get("/analytics/summary", app.analyticsSummary)
		r.With(app.analyticsAccessMiddleware, app.cacheAnalytics(4*time.Second, "live")).Get("/analytics/live", app.analyticsLive)
		r.With(app.analyticsAccessMiddleware, app.cacheAnalytics(30*time.Second, "fraud")).Get("/analytics/fraud", app.analyticsFraud)
		r.With(app.analyticsAccessMiddleware, app.cacheAnalytics(10*time.Second, "conversions")).Get("/analytics/conversions", app.analyticsConversions)
		r.With(app.analyticsAccessMiddleware).Post("/analytics/conversions", app.analyticsConversions)
		r.With(app.analyticsAccessMiddleware).Get("/analytics/report", app.analyticsReport)
	})
	r.Get("/link/{slug}", app.redirectWithAnalytics)
	r.Handle("/track/conversion/{slug}", http.HandlerFunc(app.recordConversion))

	server := &http.Server{
		Addr:              ":" + os.Getenv("PORT"),
		Handler:           r,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Println("Erreur lors du lancement: ", err)
		return
	}
}
