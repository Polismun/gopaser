package main

import (
	"io"
	"net/http"
)

const gcBotURL = "http://127.0.0.1:3001"

// GET /gc/demo-url?matchId=X&outcomeId=Y&token=Z
// Proxies to the local Steam GC bot. Auth-gated via verify-steam-sync
// (same as /steam/fetch-parse) to prevent public abuse.
func handleGCProxy(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}

	if !limiter.allow(clientIP(r)) {
		http.Error(w, "Rate limited", http.StatusTooManyRequests)
		return
	}

	// Auth: same verify endpoint as /steam/fetch-parse
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

	// Forward to local GC bot
	botReq, err := http.NewRequest("GET", gcBotURL+"/gc/demo-url?"+r.URL.RawQuery, nil)
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	resp, err := verifyClient.Do(botReq)
	if err != nil {
		http.Error(w, "GC bot unreachable", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	io.Copy(w, resp.Body)
}
