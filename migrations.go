package main

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

func runMigrations(db *sql.DB) error {
	// 1. Create migrations table if it doesn't exist
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version TEXT PRIMARY KEY)`)
	if err != nil {
		return fmt.Errorf("failed to create migrations table: %w", err)
	}

	// 2. Read migration files
	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("failed to read migrations directory: %w", err)
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)

	// 3. Apply migrations
	for _, file := range files {
		var version string
		err := db.QueryRow("SELECT version FROM schema_migrations WHERE version = ?", file).Scan(&version)
		if err == sql.ErrNoRows {
			fmt.Printf("Applying migration: %s\n", file)
			content, err := migrationFiles.ReadFile("migrations/" + file)
			if err != nil {
				return fmt.Errorf("failed to read migration file %s: %w", file, err)
			}

			tx, err := db.Begin()
			if err != nil {
				return err
			}

			if _, err := tx.Exec(string(content)); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to execute migration %s: %w", file, err)
			}

			if _, err := tx.Exec("INSERT INTO schema_migrations (version) VALUES (?)", file); err != nil {
				tx.Rollback()
				return fmt.Errorf("failed to record migration %s: %w", file, err)
			}

			if err := tx.Commit(); err != nil {
				return err
			}
		} else if err != nil {
			return fmt.Errorf("failed to check migration %s: %w", file, err)
		}
	}

	return nil
}
