package db

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"mnemo/internal/config"

	_ "modernc.org/sqlite"
)

type Store struct {
	DB *sql.DB
}

func NewStore() (*Store, error) {
	targetDir := config.Current.DataDir
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Printf("Warning: Directory %s inaccessible, falling back to ./data\n", targetDir)
		targetDir = "./data"
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create data directory: %w", err)
		}
	}

	dbPath := filepath.Join(targetDir, "mnemo.db")
	log.Printf("Mnemonic Storage Engine: %s", dbPath)

	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous=NORMAL&_pragma=foreign_keys=ON", dbPath)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open db: %w", err)
	}

	if _, err := db.Exec(Schema); err != nil {
		return nil, fmt.Errorf("failed to run schema: %w", err)
	}
	if _, err := db.Exec(SeedDefaultSettings); err != nil {
		return nil, fmt.Errorf("failed to seed settings: %w", err)
	}

	return &Store{DB: db}, nil
}
