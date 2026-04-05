package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"regexp"
	"time"
)

// Anti-SSRF: only allow Valve replay CDN hosts matching this exact pattern.
var valveReplayUrlRe = regexp.MustCompile(`^https?://replay\d+\.valve\.net/730/\d+_\d+\.dem\.bz2$`)

type steamFetchRequest struct {
	URL       string `json:"url"`
	Sharecode string `json:"sharecode"`
}

type steamFetchResponse struct {
	Skipped    bool   `json:"skipped"`
	ID         string `json:"id"`
	Hash       string `json:"hash"`
	SizeBytes  int64  `json:"sizeBytes"`
	RecordedAt string `json:"recordedAt,omitempty"` // ISO-8601, from Valve CDN Last-Modified
}

// POST /steam/fetch-parse
// Body: { url, sharecode }
// Auth: Firebase Bearer → verify-steam-sync on Vercel (checks canAutoImportDemos)
//
// Flow: download bz2 → bunzip → SHA-256 → hash dedup check → if new, parse + save.
// Returns { skipped, id, hash, sizeBytes }.
func handleSteamFetchParse(w http.ResponseWriter, r *http.Request) {
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
	status, err := verifyAuthAt(authHeader, "/api/verify-steam-sync")
	if status != http.StatusOK {
		msg := "Unauthorized"
		if err != nil {
			msg = err.Error()
		}
		http.Error(w, msg, status)
		return
	}

	var body steamFetchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	if !valveReplayUrlRe.MatchString(body.URL) {
		http.Error(w, "Invalid replay URL", http.StatusBadRequest)
		return
	}

	// 1. Download bz2 + bunzip + SHA-256 (streaming)
	dl, err := downloadAndDecompressBz2(body.URL)
	if err != nil {
		if isDemoNotFound(err) {
			http.Error(w, "Demo expired or missing", http.StatusNotFound)
			return
		}
		log.Printf("[steam-fetch-parse] download failed: %v", err)
		http.Error(w, "Download failed", http.StatusBadGateway)
		return
	}
	defer os.Remove(dl.tmpPath)

	// 2. Dedup check via Vercel
	exists, existingVpsFileId, err := checkDemoHashViaVercel(dl.sha256Hex, authHeader)
	if err != nil {
		log.Printf("[steam-fetch-parse] hash check failed: %v", err)
		http.Error(w, "Hash check failed", http.StatusBadGateway)
		return
	}
	if exists {
		writeSteamFetchJSON(w, steamFetchResponse{
			Skipped:    true,
			ID:         existingVpsFileId,
			Hash:       dl.sha256Hex,
			SizeBytes:  0,
			RecordedAt: dl.recordedAt,
		})
		return
	}

	// 3. Acquire parse semaphore (same pattern as parsemulti.go per-demo)
	ctx, cancel := context.WithTimeout(r.Context(), queueTimeout)
	defer cancel()

	select {
	case parseSem <- struct{}{}:
	case <-ctx.Done():
		http.Error(w, "Queue timeout", http.StatusServiceUnavailable)
		return
	}

	start := time.Now()
	result, err := parseAndSave(extractedDem{path: dl.tmpPath, name: "steam-" + body.Sharecode + ".dem"})
	<-parseSem

	if err != nil {
		log.Printf("[steam-fetch-parse] parse failed for %s: %v", body.Sharecode, err)
		http.Error(w, "Parse failed", http.StatusInternalServerError)
		return
	}
	log.Printf("[steam-fetch-parse] parsed %s in %s (id=%s, %d bytes)", body.Sharecode, time.Since(start), result.ID, result.SizeBytes)

	writeSteamFetchJSON(w, steamFetchResponse{
		Skipped:    false,
		ID:         result.ID,
		Hash:       dl.sha256Hex,
		SizeBytes:  result.SizeBytes,
		RecordedAt: dl.recordedAt,
	})
}

func writeSteamFetchJSON(w http.ResponseWriter, resp steamFetchResponse) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
