package main

import (
	"compress/gzip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func demoFilePath(id string) string {
	return filepath.Join(demosDir, id+".json.gz")
}

// validateDemoID checks that the ID is a valid hex string (no path traversal)
func validateDemoID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// POST /demo/save — save parsed JSON to disk (gzipped)
func handleDemoSave(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	if !limiter.allow(clientIP(r)) {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	status, err := verifyAuth(authHeader)
	if status != http.StatusOK {
		errMsg := "Unauthorized"
		if err != nil {
			errMsg = err.Error()
		}
		http.Error(w, errMsg, status)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxDemoSaveBytes)

	id := generateUUID()
	path := demoFilePath(id)

	// Write gzipped to temp file, then rename (atomic)
	tmpFile, err := os.CreateTemp(demosDir, "tmp-*.gz")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	tmpName := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpName) // cleanup on error; no-op after rename
	}()

	gz := gzip.NewWriter(tmpFile)
	n, err := io.Copy(gz, r.Body)
	if err != nil {
		http.Error(w, "File too large or upload failed", http.StatusRequestEntityTooLarge)
		return
	}
	if err := gz.Close(); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
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

	// Get compressed size
	fi, err := os.Stat(tmpName)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	sizeBytes := fi.Size()

	if err := os.Rename(tmpName, path); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	log.Printf("Demo saved: %s (%d bytes raw, %d bytes gzipped)", id, n, sizeBytes)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"id":        id,
		"sizeBytes": sizeBytes,
	})
}

// GET /demo/{id} and DELETE /demo/{id}
func handleDemoRoute(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Extract ID from /demo/{id}
	id := strings.TrimPrefix(r.URL.Path, "/demo/")
	if !validateDemoID(id) {
		http.Error(w, "Invalid demo ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		handleDemoGet(w, r, id)
	case http.MethodDelete:
		handleDemoDelete(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// GET /demo/{id} — serve gzipped JSON (visibility-checked via Vercel)
func handleDemoGet(w http.ResponseWriter, r *http.Request, id string) {
	if !readLimiter.allow(clientIP(r)) {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	// Verify visibility via Vercel (cached 30s)
	authHeader := r.Header.Get("Authorization")
	status, err := verifyDemoRead(id, authHeader)
	if status != http.StatusOK {
		errMsg := "Access denied"
		if err != nil {
			errMsg = err.Error()
		}
		http.Error(w, errMsg, status)
		return
	}

	path := demoFilePath(id)

	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "Demo not found", http.StatusNotFound)
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "application/json")

	// If client accepts gzip, serve raw gzipped file (fast, no decompression)
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		w.Header().Set("Content-Encoding", "gzip")
		io.Copy(w, f)
		return
	}

	// Otherwise decompress for the client
	gz, err := gzip.NewReader(f)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer gz.Close()
	io.Copy(w, gz)
}

// DELETE /demo/{id} — auth + ownership required
func handleDemoDelete(w http.ResponseWriter, r *http.Request, id string) {
	if !limiter.allow(clientIP(r)) {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}

	// Verify ownership via Vercel /api/verify-demo-delete
	status, keepFile, err := verifyDemoDelete(id, authHeader)
	if status != http.StatusOK {
		errMsg := "Unauthorized"
		if err != nil {
			errMsg = err.Error()
		}
		http.Error(w, errMsg, status)
		return
	}

	// If other DemoDocs still reference this VPS file, keep it on disk
	if keepFile {
		log.Printf("Demo delete skipped (shared file): %s", id)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
		return
	}

	path := demoFilePath(id)
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Demo not found", http.StatusNotFound)
		} else {
			http.Error(w, "Internal error", http.StatusInternalServerError)
		}
		return
	}

	log.Printf("Demo deleted: %s", id)
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("ok"))
}

// POST /cleanup-orphans — admin-only, deletes VPS demo files with no Firestore DemoDoc.
// Query param ?dryRun=true lists orphans without deleting them.
func handleCleanupOrphans(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Admin auth via verify-admin (checks isAdmin, not just canUploadDemos)
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if verifyURL != "" {
		status, err := verifyAuthAt(authHeader, "/api/verify-admin")
		if status != http.StatusOK {
			msg := "Unauthorized"
			if err != nil {
				msg = err.Error()
			}
			http.Error(w, msg, status)
			return
		}
	}

	dryRun := r.URL.Query().Get("dryRun") == "true"

	entries, err := os.ReadDir(demosDir)
	if err != nil {
		http.Error(w, "Failed to list demos", http.StatusInternalServerError)
		return
	}

	var deleted []string
	kept := 0

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".json.gz") {
			continue
		}
		id := strings.TrimSuffix(name, ".json.gz")

		// Check if DemoDoc exists via verify-demo-read (no auth → 404 if missing, 403 if private)
		status, _ := verifyDemoRead(id, "")
		if status == http.StatusNotFound {
			if dryRun {
				deleted = append(deleted, id)
				log.Printf("[cleanup-orphans] Would delete orphan: %s (dry-run)", id)
			} else {
				path := filepath.Join(demosDir, name)
				if err := os.Remove(path); err == nil {
					deleted = append(deleted, id)
					log.Printf("[cleanup-orphans] Deleted orphan: %s", id)
				}
			}
		} else {
			kept++
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"deleted": deleted,
		"kept":    kept,
		"dryRun":  dryRun,
	})
}
