package main

import (
	"log"
	"net/http"

	"mnemo/internal/config"
	"mnemo/internal/db"
	"mnemo/internal/server"
)

func main() {
	config.Parse()

	store, err := db.NewStore()
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer store.DB.Close()

	router := server.NewRouter(store)

	wrappedRouter := server.HTMXMiddleware(router)

	addr := ":" + config.Current.Port
	log.Printf("Mnemo server starting on %s", addr)
	if err := http.ListenAndServe(addr, wrappedRouter); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}
