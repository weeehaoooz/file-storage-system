package main

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

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

	// Check if this is a chunked upload
	chunkIndexStr := r.URL.Query().Get("chunkIndex")
	if chunkIndexStr != "" {
		chunkIndex, err := strconv.Atoi(chunkIndexStr)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid chunkIndex")
			return
		}
		totalChunks, err := strconv.Atoi(r.URL.Query().Get("totalChunks"))
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid totalChunks")
			return
		}
		chunkSize, err := strconv.ParseInt(r.URL.Query().Get("chunkSize"), 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "Invalid chunkSize")
			return
		}

		log.Printf("Received chunk %d of %d for file %s", chunkIndex+1, totalChunks, id)

		expectedHash := r.Header.Get("X-Content-SHA256")
		if expectedHash == "" {
			writeError(w, http.StatusBadRequest, "X-Content-SHA256 header is required for chunk upload")
			return
		}

		// Read chunk body and compute hash
		var buf bytes.Buffer
		hasher := sha256.New()
		tee := io.TeeReader(r.Body, &buf)
		if _, err := io.Copy(hasher, tee); err != nil {
			log.Printf("Failed to read chunk: %v", err)
			writeError(w, http.StatusInternalServerError, "Failed to read chunk body")
			return
		}

		calculatedHash := hex.EncodeToString(hasher.Sum(nil))
		if calculatedHash != expectedHash {
			log.Printf("Hash mismatch for chunk %d: expected %s, got %s", chunkIndex, expectedHash, calculatedHash)
			writeError(w, http.StatusBadRequest, "Chunk hash mismatch")
			return
		}

		destPath := filepath.Join(StorageDir, id)
		destFile, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			log.Printf("Failed to open destination file for chunk: %v", err)
			writeError(w, http.StatusInternalServerError, "Failed to open destination file")
			return
		}
		defer destFile.Close()

		offset := int64(chunkIndex) * chunkSize
		_, err = destFile.WriteAt(buf.Bytes(), offset)
		if err != nil {
			log.Printf("Failed to write chunk at offset %d: %v", offset, err)
			writeError(w, http.StatusInternalServerError, "Failed to write chunk to disk")
			return
		}

		writeJSON(w, http.StatusOK, map[string]string{
			"message": fmt.Sprintf("Chunk %d uploaded successfully", chunkIndex),
			"fileId":  id,
		})
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

func handleUploadComplete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "File ID is required")
		return
	}

	record, err := GetFile(id)
	if err != nil {
		if err == sql.ErrNoRows {
			writeError(w, http.StatusNotFound, "File record not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "Failed to retrieve file record")
		return
	}

	destPath := filepath.Join(StorageDir, id)
	fi, err := os.Stat(destPath)
	if err != nil {
		log.Printf("Failed to stat completed file: %v", err)
		writeError(w, http.StatusBadRequest, "Physical file not found on disk")
		return
	}

	if fi.Size() != record.Size {
		log.Printf("File size mismatch: expected %d, got %d", record.Size, fi.Size())
		writeError(w, http.StatusBadRequest, fmt.Sprintf("File size mismatch. Expected %d bytes, got %d bytes", record.Size, fi.Size()))
		return
	}

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
