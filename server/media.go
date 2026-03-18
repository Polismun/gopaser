package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	mediaDir          = "media"
	maxMediaSaveBytes = 200 << 20 // 200 MB
	mediaRateMax      = 30        // 30 req/min per IP (higher than parse — a publish can have 20+ files)
)

// Separate rate limiter for media endpoints (higher limit than parse/demo)
var mediaLimiter = &rateLimiter{
	requests: make(map[string][]time.Time),
	mu:       sync.Mutex{},
	max:      mediaRateMax,
}

// MIME type → file extension mapping
var mimeToExt = map[string]string{
	"image/jpeg":    ".jpg",
	"image/png":     ".png",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
	"video/mp4":     ".mp4",
	"video/webm":    ".webm",
}

// File extension → MIME type mapping (reverse of mimeToExt)
var extToMime = map[string]string{
	".jpg":  "image/jpeg",
	".jpeg": "image/jpeg",
	".png":  "image/png",
	".webp": "image/webp",
	".svg":  "image/svg+xml",
	".mp4":  "video/mp4",
	".webm": "video/webm",
}

// Valid media categories (subdirectories under media/)
var validCategories = map[string]bool{
	"images": true,
	"videos": true,
}

// validateMediaID checks that the ID is a valid media filename.
// Format: {32-char-hex}.{ext} or {32-char-hex}_thumb.{ext}
// No path traversal characters allowed.
func validateMediaID(id string) bool {
	if len(id) < 36 || len(id) > 42 {
		return false
	}
	if strings.Contains(id, "..") || strings.Contains(id, "/") || strings.Contains(id, "\\") {
		return false
	}

	// Split at the last dot to get base and extension
	dotIdx := strings.LastIndex(id, ".")
	if dotIdx < 0 {
		return false
	}
	base := id[:dotIdx]
	ext := id[dotIdx:]

	// Extension must be recognized
	if _, ok := extToMime[ext]; !ok {
		return false
	}

	// Base must be 32-char hex, optionally followed by "_thumb"
	hexPart := base
	if strings.HasSuffix(base, "_thumb") {
		hexPart = base[:len(base)-6] // strip "_thumb"
	}

	if len(hexPart) != 32 {
		return false
	}
	for _, c := range hexPart {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// POST /media/save — save a media file (image or video) to disk
func handleMediaSave(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	if !mediaLimiter.allow(clientIP(r)) {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	status, err := verifyAuthAt(authHeader, "/api/verify-media")
	if status != http.StatusOK {
		errMsg := "Unauthorized"
		if err != nil {
			errMsg = err.Error()
		}
		http.Error(w, errMsg, status)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxMediaSaveBytes)

	// Validate Content-Type
	contentType := r.Header.Get("Content-Type")
	ext, ok := mimeToExt[contentType]
	if !ok {
		http.Error(w, "Unsupported Content-Type: "+contentType, http.StatusBadRequest)
		return
	}

	// Validate category header
	category := r.Header.Get("X-Media-Category")
	if !validCategories[category] {
		http.Error(w, "Invalid or missing X-Media-Category header (must be 'images' or 'videos')", http.StatusBadRequest)
		return
	}

	id := generateUUID()
	filename := id + ext
	destDir := filepath.Join(mediaDir, category)
	destPath := filepath.Join(destDir, filename)

	// Write to temp file, then rename (atomic)
	tmpFile, err := os.CreateTemp(destDir, "tmp-*")
	if err != nil {
		log.Printf("[media] Failed to create temp file in %s: %v", destDir, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	tmpName := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpName) // cleanup on error; no-op after rename
	}()

	n, err := io.Copy(tmpFile, r.Body)
	if err != nil {
		http.Error(w, "File too large or upload failed", http.StatusRequestEntityTooLarge)
		return
	}
	if err := tmpFile.Sync(); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	if err := tmpFile.Close(); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := os.Rename(tmpName, destPath); err != nil {
		log.Printf("[media] Failed to rename %s → %s: %v", tmpName, destPath, err)
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("Media saved: %s/%s (%d bytes)", category, filename, n)

	// Build public URL
	publicURL := ""
	if verifyURL != "" {
		// In prod, the domain is derived from the VPS public URL
		// The client knows the base URL via NEXT_PUBLIC_PARSER_URL
		publicURL = "/media/" + category + "/" + filename
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        filename,
		"url":       publicURL,
		"sizeBytes": n,
	})
}

// handleMediaRoute dispatches GET and DELETE for /media/{category}/{id}
func handleMediaRoute(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Parse /media/{category}/{id} from URL path
	path := strings.TrimPrefix(r.URL.Path, "/media/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		http.Error(w, "Invalid path: expected /media/{category}/{id}", http.StatusBadRequest)
		return
	}

	category := parts[0]
	id := parts[1]

	if !validCategories[category] {
		http.Error(w, "Invalid category: "+category, http.StatusBadRequest)
		return
	}
	if !validateMediaID(id) {
		http.Error(w, "Invalid media ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		handleMediaGet(w, r, category, id)
	case http.MethodDelete:
		handleMediaDelete(w, r, category, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /media/{category}/{id} — serve media file (public, no auth)
func handleMediaGet(w http.ResponseWriter, _ *http.Request, category, id string) {
	filePath := filepath.Join(mediaDir, category, id)

	f, err := os.Open(filePath)
	if err != nil {
		http.Error(w, "Media not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	// Determine Content-Type from file extension
	ext := filepath.Ext(id)
	mime, ok := extToMime[ext]
	if !ok {
		mime = "application/octet-stream"
	}

	w.Header().Set("Content-Type", mime)
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	io.Copy(w, f)
}

// DELETE /media/{category}/{id} — auth + strat ownership required
func handleMediaDelete(w http.ResponseWriter, r *http.Request, category, id string) {
	if !mediaLimiter.allow(clientIP(r)) {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	stratId := r.Header.Get("X-Strat-Id")
	if stratId == "" && verifyURL != "" {
		http.Error(w, "Missing X-Strat-Id header", http.StatusBadRequest)
		return
	}

	// Verify strat ownership via Vercel /api/verify-media-delete
	status, err := verifyMediaDelete(stratId, authHeader)
	if status != http.StatusOK {
		errMsg := "Unauthorized"
		if err != nil {
			errMsg = err.Error()
		}
		http.Error(w, errMsg, status)
		return
	}

	filePath := filepath.Join(mediaDir, category, id)
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Media not found", http.StatusNotFound)
		} else {
			http.Error(w, "Internal error", http.StatusInternalServerError)
		}
		return
	}

	log.Printf("Media deleted: %s/%s", category, id)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}
