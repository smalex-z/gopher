package main

import (
	"embed"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/smalex-z/gopher/internal/api"
	"github.com/smalex-z/gopher/internal/db"
)

//go:embed frontend
var frontendFS embed.FS

func main() {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "gopher.db"
	}

	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer database.Close()

	if err := db.Migrate(database); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	r := mux.NewRouter()
	api.RegisterRoutes(r, database)

	// Serve frontend
	r.PathPrefix("/").Handler(http.FileServer(http.FS(frontendFS)))

	addr := ":8080"
	log.Printf("Gopher listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
