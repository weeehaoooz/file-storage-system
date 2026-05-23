package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/google/uuid"
)

// StorageDir is the path where raw files are saved
var StorageDir = func() string {
	if val := os.Getenv("STORAGE_DIR"); val != "" {
		return val
	}
	return "storage-disk"
}()

var APIServerURL = func() string {
	if val := os.Getenv("API_SERVER_URL"); val != "" {
		return val
	}
	return "http://localhost:3000"
}()

// --- CORS Middleware ---
func enableCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- helper: JSON responses ---
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// ==========================================
// API Server Handlers (Port 3000)
// ==========================================

func handleGetFiles(w http.ResponseWriter, r *http.Request) {
	files, err := ListFiles()
	if err != nil {
		log.Printf("Error listing files: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to list files")
		return
	}
	if files == nil {
		files = []FileRecord{}
	}
	writeJSON(w, http.StatusOK, files)
}

func handlePresign(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		Type string `json:"type"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.Name == "" || req.Size < 0 {
		writeError(w, http.StatusBadRequest, "File name and valid size are required")
		return
	}

	fileID := uuid.New().String()
	record := FileRecord{
		ID:     fileID,
		Name:   req.Name,
		Size:   req.Size,
		Type:   req.Type,
		Status: "pending",
	}

	if err := InsertFile(record); err != nil {
		log.Printf("Error inserting file metadata: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to register upload")
		return
	}

	uploadURL := "/storage/upload/" + fileID
	writeJSON(w, http.StatusOK, map[string]string{
		"uploadUrl": uploadURL,
		"fileId":    fileID,
	})
}

func handleRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.ID == "" || req.Name == "" {
		writeError(w, http.StatusBadRequest, "ID and Name are required")
		return
	}

	if err := UpdateFileName(req.ID, req.Name); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "File not found")
			return
		}
		log.Printf("Error updating file name: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to rename file")
		return
	}

	record, err := GetFile(req.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Failed to retrieve updated file")
		return
	}

	writeJSON(w, http.StatusOK, record)
}

func handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "File ID is required")
		return
	}

	// Delete from physical storage
	filePath := filepath.Join(StorageDir, id)
	_ = os.Remove(filePath) // Ignore error if file doesn't exist on disk yet

	// Delete from database
	if err := DeleteFile(id); err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "File not found")
			return
		}
		log.Printf("Error deleting file metadata: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to delete file record")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func handleInternalUploadComplete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID string `json:"id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "File ID is required")
		return
	}

	if err := UpdateFileStatus(req.ID, "active"); err != nil {
		log.Printf("Failed to update status to active for ID %s: %v", req.ID, err)
		writeError(w, http.StatusInternalServerError, "Failed to activate file record")
		return
	}

	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

// ==========================================
// Storage Server Handlers (Port 3001)
// ==========================================

func handleUpload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "File ID is required")
		return
	}

	// Verify file is registered in database as pending
	record, err := GetFile(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "Upload not pre-registered")
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to verify upload registry")
		return
	}

	if record.Status != "pending" {
		writeError(w, http.StatusConflict, "File has already been uploaded or is invalid")
		return
	}

	// Create storage directory if not exists
	if err := os.MkdirAll(StorageDir, 0755); err != nil {
		log.Printf("Error creating storage folder: %v", err)
		writeError(w, http.StatusInternalServerError, "Internal storage creation failed")
		return
	}

	// Open destination file
	destPath := filepath.Join(StorageDir, id)
	destFile, err := os.Create(destPath)
	if err != nil {
		log.Printf("Failed to create file on disk: %v", err)
		writeError(w, http.StatusInternalServerError, "Failed to initialize file writing")
		return
	}
	defer destFile.Close()

	// Copy body (raw binary PUT stream) to file
	_, err = io.Copy(destFile, r.Body)
	if err != nil {
		log.Printf("Failed to stream copy request body to file: %v", err)
		writeError(w, http.StatusInternalServerError, "Upload stream write failure")
		return
	}

	// Notify main API server that upload is complete
	callbackURL := APIServerURL + "/api/internal/upload-complete"
	notifyData, _ := json.Marshal(map[string]string{"id": id})
	resp, err := http.Post(callbackURL, "application/json", bytes.NewBuffer(notifyData))
	if err != nil {
		log.Printf("Callback to API server failed: %v", err)
		writeError(w, http.StatusInternalServerError, "Callback notification failed")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Callback API server returned status: %d", resp.StatusCode)
		writeError(w, http.StatusInternalServerError, "Failed to confirm upload internally")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"message": "Upload complete!",
		"fileId":  id,
	})
}

func handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "File ID is required")
		return
	}

	record, err := GetFile(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "File metadata not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to query database")
		return
	}

	filePath := filepath.Join(StorageDir, id)
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		writeError(w, http.StatusNotFound, "Physical file not found on disk")
		return
	}

	// Set original metadata headers
	w.Header().Set("Content-Type", record.Type)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+record.Name+"\"")

	// Serve the file directly
	http.ServeFile(w, r, filePath)
}
