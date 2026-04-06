package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// verifyURL is set once at startup; empty only if DEV_MODE=true
var verifyURL string

// verifyClient is used for all auth/verify HTTP calls (with timeout to avoid hanging).
var verifyClient = &http.Client{Timeout: 10 * time.Second}

// --- Rate limiter (sliding window, 10 req/min per IP) ---

type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	max      int // max requests per window (default: rateMaxDefault)
}

var limiter = &rateLimiter{requests: make(map[string][]time.Time), max: rateMaxDefault}

// Separate rate limiter for demo reads (higher limit — reads are more frequent)
var readLimiter = &rateLimiter{requests: make(map[string][]time.Time), max: rateMaxRead}

const (
	rateWindow                 = time.Minute
	rateMaxDefault             = 10
	maxBodyBytes               = 3 << 30         // 3 GB (BO5 .rar can be 1.5-2 GB)
	maxConcurrent              = 2               // ~68 MB peak heap per parse (v5 + spiller fix) — RAM not the bottleneck anymore
	queueTimeout               = 5 * time.Minute // max wait time in parsing queue (large demos need more time)
	maxDemoSaveBytes           = 200 << 20       // 200 MB for parsed JSON
	rateMaxRead                = 60              // 60 req/min per IP for demo reads
	demosDir                   = "demos"
	maxDecompressedBytes int64 = 5 << 30 // 5 GB decompressed limit (BO5: 5 × ~800 MB .dem)
)

// Semaphore limits concurrent parser processes
var parseSem = make(chan struct{}, maxConcurrent)

// Queue tracking for /queue endpoint
var queueWaiting atomic.Int32

func (rl *rateLimiter) allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rateWindow)

	// Remove expired entries
	valid := rl.requests[key][:0]
	for _, t := range rl.requests[key] {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.max {
		rl.requests[key] = valid
		return false
	}

	rl.requests[key] = append(valid, now)
	return true
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	verifyURL = os.Getenv("VERIFY_URL")
	if verifyURL == "" && os.Getenv("DEV_MODE") != "true" {
		log.Fatal("VERIFY_URL is required (set DEV_MODE=true to skip)")
	}

	// Ensure storage directories exist
	if err := os.MkdirAll(demosDir, 0755); err != nil {
		log.Fatalf("Cannot create demos directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mediaDir, "images"), 0755); err != nil {
		log.Fatalf("Cannot create media/images directory: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(mediaDir, "videos"), 0755); err != nil {
		log.Fatalf("Cannot create media/videos directory: %v", err)
	}

	// Cleanup stale temp files from previous crashes
	cleanupTempFiles()

	http.HandleFunc("/parse-multi", handleParseMulti)
	http.HandleFunc("/parse", handleParse)
	http.HandleFunc("/steam/fetch-parse", handleSteamFetchParse)
	http.HandleFunc("/gc/demo-url", handleGCProxy)
	http.HandleFunc("/demo/save", handleDemoSave)
	http.HandleFunc("/demo/", handleDemoRoute)
	http.HandleFunc("/media/save", handleMediaSave)
	http.HandleFunc("/media/", handleMediaRoute)
	http.HandleFunc("/cleanup-orphans", handleCleanupOrphans)
	http.HandleFunc("/queue", handleQueue)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w)
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("Parser server listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

// --- Shared helpers ---

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, GET, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Demo-Owner, Content-Encoding, X-Media-Category, X-Demo-Id, X-Strat-Id")
}

// verifyAuthAt verifies auth by calling the given API path on the Vercel app.
func verifyAuthAt(authHeader string, apiPath string) (int, error) {
	if verifyURL == "" {
		return http.StatusOK, nil // DEV_MODE only
	}

	req, err := http.NewRequest("POST", verifyURL+apiPath, nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	req.Header.Set("Authorization", authHeader)

	resp, err := verifyClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("auth service unreachable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return resp.StatusCode, fmt.Errorf("unauthorized")
		case http.StatusForbidden:
			return resp.StatusCode, fmt.Errorf("forbidden")
		case http.StatusTooManyRequests:
			return resp.StatusCode, fmt.Errorf("rate limited")
		default:
			return resp.StatusCode, fmt.Errorf("auth check failed")
		}
	}

	return http.StatusOK, nil
}

// verifyAuth verifies auth via the default /api/verify-upload endpoint.
func verifyAuth(authHeader string) (int, error) {
	return verifyAuthAt(authHeader, "/api/verify-upload")
}

func clientIP(r *http.Request) string {
	if ips := r.Header.Get("X-Forwarded-For"); ips != "" {
		// Take last IP in chain (added by the trusted reverse proxy, e.g. Caddy)
		parts := strings.Split(ips, ",")
		return strings.TrimSpace(parts[len(parts)-1])
	}
	return r.RemoteAddr
}

// --- Demo read verification with cache ---

type demoReadCacheEntry struct {
	status int
	expiry time.Time
}

var demoReadCache sync.Map

const demoReadCacheTTL = 30 * time.Second

// verifyDemoRead checks whether a demo can be read by calling /api/verify-demo-read.
// Results are cached for 30 seconds keyed by demoId + auth header hash.
func verifyDemoRead(demoId, authHeader string) (int, error) {
	if verifyURL == "" {
		return http.StatusOK, nil // DEV_MODE only
	}

	// Build cache key from demoId + auth (empty if no auth)
	cacheKey := demoId + "|" + authHeader
	if entry, ok := demoReadCache.Load(cacheKey); ok {
		e := entry.(demoReadCacheEntry)
		if time.Now().Before(e.expiry) {
			if e.status == http.StatusOK {
				return http.StatusOK, nil
			}
			return e.status, fmt.Errorf("cached: access denied")
		}
		demoReadCache.Delete(cacheKey)
	}

	req, err := http.NewRequest("POST", verifyURL+"/api/verify-demo-read", nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	req.Header.Set("X-Demo-Id", demoId)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := verifyClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("demo read auth service unreachable")
	}
	defer resp.Body.Close()

	// Cache the result
	demoReadCache.Store(cacheKey, demoReadCacheEntry{
		status: resp.StatusCode,
		expiry: time.Now().Add(demoReadCacheTTL),
	})

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusNotFound:
			return resp.StatusCode, fmt.Errorf("demo not found")
		case http.StatusForbidden:
			return resp.StatusCode, fmt.Errorf("access denied")
		case http.StatusUnauthorized:
			return resp.StatusCode, fmt.Errorf("unauthorized")
		default:
			return resp.StatusCode, fmt.Errorf("demo read check failed")
		}
	}

	return http.StatusOK, nil
}

// verifyDemoDelete checks whether a demo can be deleted by calling /api/verify-demo-delete.
// Returns (status, keepFile, error). keepFile=true means other DemoDocs still reference
// this VPS file, so the file should NOT be deleted from disk.
func verifyDemoDelete(demoId, authHeader string) (int, bool, error) {
	if verifyURL == "" {
		return http.StatusOK, false, nil // DEV_MODE only
	}

	req, err := http.NewRequest("POST", verifyURL+"/api/verify-demo-delete", nil)
	if err != nil {
		return http.StatusInternalServerError, false, err
	}
	req.Header.Set("X-Demo-Id", demoId)
	req.Header.Set("Authorization", authHeader)

	resp, err := verifyClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, false, fmt.Errorf("demo delete auth service unreachable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return resp.StatusCode, false, fmt.Errorf("unauthorized")
		case http.StatusForbidden:
			return resp.StatusCode, false, fmt.Errorf("forbidden: not the owner")
		default:
			return resp.StatusCode, false, fmt.Errorf("demo delete check failed")
		}
	}

	// Parse keepFile from response body
	var body struct {
		KeepFile bool `json:"keepFile"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		// If body parsing fails, default to not keeping the file (backward compat)
		return http.StatusOK, false, nil
	}

	return http.StatusOK, body.KeepFile, nil
}

// verifyMediaDelete checks whether a media file can be deleted by calling /api/verify-media-delete.
func verifyMediaDelete(stratId, authHeader string) (int, error) {
	if verifyURL == "" {
		return http.StatusOK, nil // DEV_MODE only
	}

	req, err := http.NewRequest("POST", verifyURL+"/api/verify-media-delete", nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	req.Header.Set("X-Strat-Id", stratId)
	req.Header.Set("Authorization", authHeader)

	resp, err := verifyClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("media delete auth service unreachable")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case http.StatusUnauthorized:
			return resp.StatusCode, fmt.Errorf("unauthorized")
		case http.StatusForbidden:
			return resp.StatusCode, fmt.Errorf("forbidden: not the owner")
		case http.StatusBadRequest:
			return resp.StatusCode, fmt.Errorf("missing strat ID")
		default:
			return resp.StatusCode, fmt.Errorf("media delete check failed")
		}
	}

	return http.StatusOK, nil
}
