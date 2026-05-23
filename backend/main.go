package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	// Initialize database
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "files.db"
	}
	if err := InitDB(dbPath); err != nil {
		log.Fatalf("Database initialization failed: %v", err)
	}

	// Create storage folder if not exists
	if err := os.MkdirAll(StorageDir, 0755); err != nil {
		log.Fatalf("Failed to create storage folder: %v", err)
	}

	// API Server Mux (Port 3000)
	apiMux := http.NewServeMux()
	apiMux.HandleFunc("GET /api/files", handleGetFiles)
	apiMux.HandleFunc("POST /api/files/presign", handlePresign)
	apiMux.HandleFunc("POST /api/files/rename", handleRename)
	apiMux.HandleFunc("DELETE /api/files/{id}", handleDelete)
	apiMux.HandleFunc("POST /api/internal/upload-complete", handleInternalUploadComplete)

	// Storage Server Mux (Port 3001)
	storageMux := http.NewServeMux()
	storageMux.HandleFunc("PUT /storage/upload/{id}", handleUpload)
	storageMux.HandleFunc("GET /storage/download/{id}", handleDownload)

	// Start Storage Server in goroutine
	storagePort := os.Getenv("STORAGE_PORT")
	if storagePort == "" {
		storagePort = "3001"
	}
	go func() {
		log.Printf("Starting Storage Server (Port %s)...\n", storagePort)
		err := http.ListenAndServe(":"+storagePort, enableCORS(storageMux))
		if err != nil {
			log.Fatalf("Storage Server terminated: %v", err)
		}
	}()

	// Start API Server on main thread (blocking)
	apiPort := os.Getenv("API_PORT")
	if apiPort == "" {
		apiPort = "3000"
	}
	log.Printf("Starting API Server (Port %s)...\n", apiPort)
	err := http.ListenAndServe(":"+apiPort, enableCORS(apiMux))
	if err != nil {
		log.Fatalf("API Server terminated: %v", err)
	}
}
