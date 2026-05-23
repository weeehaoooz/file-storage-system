package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "modernc.org/sqlite"
)

var db *sql.DB

type FileRecord struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	Type      string    `json:"type"`
	Status    string    `json:"status"` // "pending" or "active"
	CreatedAt time.Time `json:"createdAt"`
}

func InitDB(dbPath string) error {
	var err error
	db, err = sql.Open("sqlite", dbPath)
	if err != nil {
		return fmt.Errorf("failed to open database: %w", err)
	}

	// Ping database
	if err := db.Ping(); err != nil {
		return fmt.Errorf("failed to ping database: %w", err)
	}

	// Create table if not exists
	query := `
	CREATE TABLE IF NOT EXISTS files (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		size INTEGER NOT NULL,
		type TEXT NOT NULL,
		status TEXT NOT NULL,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	);`

	if _, err := db.Exec(query); err != nil {
		return fmt.Errorf("failed to create table: %w", err)
	}

	log.Println("Database initialized successfully.")
	return nil
}

func InsertFile(f FileRecord) error {
	query := `INSERT INTO files (id, name, size, type, status) VALUES (?, ?, ?, ?, ?)`
	_, err := db.Exec(query, f.ID, f.Name, f.Size, f.Type, f.Status)
	if err != nil {
		return fmt.Errorf("failed to insert file record: %w", err)
	}
	return nil
}

func GetFile(id string) (FileRecord, error) {
	query := `SELECT id, name, size, type, status, created_at FROM files WHERE id = ?`
	var f FileRecord
	var createdAtStr string
	err := db.QueryRow(query, id).Scan(&f.ID, &f.Name, &f.Size, &f.Type, &f.Status, &createdAtStr)
	if err != nil {
		if err == sql.ErrNoRows {
			return f, err
		}
		return f, fmt.Errorf("failed to get file record: %w", err)
	}

	// Parse date
	t, err := time.Parse("2006-01-02T15:04:05Z", createdAtStr)
	if err == nil {
		f.CreatedAt = t
	} else {
		// Try alternative formats SQLite might store
		t, err = time.Parse("2006-01-02 15:04:05", createdAtStr)
		if err == nil {
			f.CreatedAt = t
		} else {
			f.CreatedAt = time.Now() // Fallback
		}
	}

	return f, nil
}

func ListFiles() ([]FileRecord, error) {
	query := `SELECT id, name, size, type, status, created_at FROM files ORDER BY created_at DESC`
	rows, err := db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("failed to list files: %w", err)
	}
	defer rows.Close()

	var files []FileRecord
	for rows.Next() {
		var f FileRecord
		var createdAtStr string
		err := rows.Scan(&f.ID, &f.Name, &f.Size, &f.Type, &f.Status, &createdAtStr)
		if err != nil {
			return nil, fmt.Errorf("failed to scan file row: %w", err)
		}

		t, err := time.Parse("2006-01-02T15:04:05Z", createdAtStr)
		if err == nil {
			f.CreatedAt = t
		} else {
			t, err = time.Parse("2006-01-02 15:04:05", createdAtStr)
			if err == nil {
				f.CreatedAt = t
			} else {
				f.CreatedAt = time.Now()
			}
		}

		files = append(files, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows iteration error: %w", err)
	}

	return files, nil
}

func UpdateFileName(id string, name string) error {
	query := `UPDATE files SET name = ? WHERE id = ?`
	result, err := db.Exec(query, name, id)
	if err != nil {
		return fmt.Errorf("failed to update file name: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func UpdateFileStatus(id string, status string) error {
	query := `UPDATE files SET status = ? WHERE id = ?`
	result, err := db.Exec(query, status, id)
	if err != nil {
		return fmt.Errorf("failed to update file status: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}

func DeleteFile(id string) error {
	query := `DELETE FROM files WHERE id = ?`
	result, err := db.Exec(query, id)
	if err != nil {
		return fmt.Errorf("failed to delete file from DB: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rowsAffected == 0 {
		return sql.ErrNoRows
	}

	return nil
}
