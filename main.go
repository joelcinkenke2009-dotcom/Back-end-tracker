package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	_ "github.com/go-sql-driver/mysql"
	"github.com/joho/godotenv"
)

type Env struct{
	db *sql.DB
}


func(e *Env) createTables(){

	createTable := `CREATE TABLE IF NOT EXISTS user_Tracker_Link (
		id VARCHAR(255),
		name VARCHAR(250),
		first_name VARCHAR(250),
		full_name VARCHAR(250),
		password VARCHAR(255),
		email VARCHAR(250) UNIQUE NOT NULL,
		is_active_date TIMESTAMP,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err :=  e.db.Exec(createTable)
	if err != nil {
		panic(err)
	}

	createTableLink := `CREATE TABLE IF NOT EXISTS link_Tracker_Link (
		id INT AUTO_INCREMENT PRIMARY KEY,
		user_id VARCHAR(250),
		url VARCHAR(250) UNIQUE,
		slug VARCHAR(250),
		urlGenerate VARCHAR(250),
		click_total INT DEFAULT 0,
		click_mobile INT DEFAULT 0,
		click_pc INT DEFAULT 0,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);`
	_, err =  e.db.Exec(createTableLink)
	if err != nil {
		panic(err)
	}
}

func main() {
	
	if os.Getenv("ENVIRONNEMENT") == "" {
		err := godotenv.Load()
		if err != nil {
			fmt.Println("Erreur lors de l'accés au variable d'evironnement")
			return
		}	
	}
	r := chi.NewRouter()

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
	sqlDB.SetConnMaxIdleTime(25)
	sqlDB.SetConnMaxLifetime(5*time.Minute)
	
	defer sqlDB.Close()

	app := &Env{db: sqlDB}

	app.createTables()

	r.Post("/users/inscription", app.inscription)
	r.Post("/users/connexion", app.connexion)
	r.Post("/paiement/initialisation", app.paiement)
	r.Get("/paiement/verifyPaiement", app.paiementVerify)
	r.Get("/ISCONNECTED", isConnected)
	r.Get("/isActive",app.isActive)
	r.Route("/protected", func(r chi.Router) {
		r.Use(app.AuthMiddleware)
		r.Get("/data", app.dataOnly)
		r.Post("/delete",app.deleteLink)
		r.Post("/createLink", app.createLink)
		r.Post("/modify", app.modifyLink)
		r.Post("/verifyLink", verifyLink)
	})
	r.Get("/link/{slug}",app.redirectLink)

	if err := http.ListenAndServe(":"+ os.Getenv("PORT"), r); err != nil {
		fmt.Println("Erreur lors du lancement: ",err)
		return
	}
}