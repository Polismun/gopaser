package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	fhttp "github.com/bogdanfinn/fhttp"
	tls_client "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
	"github.com/nwaples/rardecode/v2"
)

// RAR magic bytes: 0x52 0x61 0x72
var rarMagic = []byte{0x52, 0x61, 0x72}

// TLS client with Chrome fingerprint (bypasses Cloudflare TLS fingerprinting)
var chromeTLS tls_client.HttpClient

func init() {
	var err error
	chromeTLS, err = tls_client.NewHttpClient(nil,
		tls_client.WithClientProfile(profiles.Chrome_146),
		tls_client.WithTimeoutSeconds(300),
	)
	if err != nil {
		log.Fatalf("Failed to create TLS client: %v", err)
	}
}

type extractedDem struct {
	path string // temp file path
	name string // original filename (e.g. "navi-vs-faze-de_mirage.dem")
}

type parsedDemo struct {
	ID        string `json:"id"`
	SizeBytes int64  `json:"sizeBytes"`
	DemName   string `json:"demName"`
}

// POST /parse-url — download demo archive from HLTV URL, extract all .dem files, parse each, save JSON.
// Uses Chrome TLS fingerprint to bypass Cloudflare.
func handleParseURL(w http.ResponseWriter, r *http.Request) {
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

	// Parse JSON body { "url": "https://..." }
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "Invalid JSON body", http.StatusBadRequest)
		return
	}
	r.Body.Close()

	// Normalize relative HLTV paths (e.g. "/download/demo/123")
	demoURL := body.URL
	if strings.HasPrefix(demoURL, "/") {
		demoURL = "https://www.hltv.org" + demoURL
	}

	if err := validateDemoURL(demoURL); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// --- Phase 1: Download + extract ALL .dem files BEFORE semaphore ---

	dems, err := downloadAndExtractAll(r.Context(), demoURL)
	if err != nil {
		log.Printf("[parse-url] download/extract failed: %v", err)
		http.Error(w, fmt.Sprintf("Download failed: %v", err), http.StatusBadGateway)
		return
	}
	defer func() {
		for _, d := range dems {
			os.Remove(d.path)
		}
	}()

	if len(dems) == 0 {
		http.Error(w, "No .dem files found", http.StatusBadRequest)
		return
	}

	// --- Phase 2: Wait in queue for parser slot ---

	queueWaiting.Add(1)
	ctx, cancel := context.WithTimeout(r.Context(), queueTimeout)
	defer cancel()

	select {
	case parseSem <- struct{}{}:
		queueWaiting.Add(-1)
		defer func() { <-parseSem }()
	case <-ctx.Done():
		queueWaiting.Add(-1)
		http.Error(w, `{"error":"queue_timeout","message":"Server busy, queue timed out"}`, http.StatusServiceUnavailable)
		return
	}

	// --- Phase 3: Parse each .dem + save (sequential, under same semaphore slot) ---

	var results []parsedDemo
	for _, dem := range dems {
		result, err := parseAndSave(dem)
		if err != nil {
			log.Printf("[parse-url] failed to parse %s: %v", dem.name, err)
			continue
		}
		results = append(results, result)
		os.Remove(dem.path)
	}

	if len(results) == 0 {
		http.Error(w, "All demos failed to parse", http.StatusInternalServerError)
		return
	}

	log.Printf("[parse-url] Parsed %d/%d demos", len(results), len(dems))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"demos": results,
	})
}

// downloadAndExtractAll downloads from HLTV URL using Chrome TLS fingerprint,
// detects format, extracts all .dem files.
func downloadAndExtractAll(ctx context.Context, rawURL string) ([]extractedDem, error) {
	req, err := fhttp.NewRequest("GET", rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://www.hltv.org/")

	resp, err := chromeTLS.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download returned HTTP %d", resp.StatusCode)
	}

	// Save to temp file with download size limit
	dlTmp, err := os.CreateTemp("", "demo-dl-*")
	if err != nil {
		return nil, err
	}
	dlTmpName := dlTmp.Name()

	limited := io.LimitReader(resp.Body, maxDownloadBytes)
	if _, err := io.Copy(dlTmp, limited); err != nil {
		dlTmp.Close()
		os.Remove(dlTmpName)
		return nil, fmt.Errorf("download write failed: %w", err)
	}
	dlTmp.Close()

	// Detect format via magic bytes
	format, err := detectFormat(dlTmpName)
	if err != nil {
		os.Remove(dlTmpName)
		return nil, err
	}

	switch format {
	case "rar":
		dems, err := extractAllDemsFromRAR(dlTmpName)
		os.Remove(dlTmpName)
		if err != nil {
			return nil, fmt.Errorf("RAR extraction failed: %w", err)
		}
		return dems, nil

	case "zstd":
		demPath, err := decompressZstd(dlTmpName)
		os.Remove(dlTmpName)
		if err != nil {
			return nil, fmt.Errorf("zstd decompression failed: %w", err)
		}
		return []extractedDem{{path: demPath, name: "demo.dem"}}, nil

	default:
		return []extractedDem{{path: dlTmpName, name: "demo.dem"}}, nil
	}
}

// parseAndSave runs the parser on a .dem file, captures output, gzips to demos/, returns metadata.
func parseAndSave(dem extractedDem) (parsedDemo, error) {
	cmd := exec.Command("./parser", dem.path)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return parsedDemo{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return parsedDemo{}, err
	}

	if err := cmd.Start(); err != nil {
		return parsedDemo{}, err
	}

	outTmp, err := os.CreateTemp("", "parse-out-*.json")
	if err != nil {
		cmd.Process.Kill()
		return parsedDemo{}, err
	}
	defer os.Remove(outTmp.Name())

	io.Copy(outTmp, stdout)

	stderrBytes, _ := io.ReadAll(stderr)
	if err := cmd.Wait(); err != nil {
		outTmp.Close()
		return parsedDemo{}, fmt.Errorf("parser error: %v | stderr: %s", err, string(stderrBytes))
	}
	outTmp.Close()

	// Gzip the parsed JSON to demos/{uuid}.json.gz
	id := generateUUID()
	destPath := demoFilePath(id)

	gzTmp, err := os.CreateTemp(demosDir, "tmp-*.gz")
	if err != nil {
		return parsedDemo{}, err
	}
	gzTmpName := gzTmp.Name()
	defer func() {
		gzTmp.Close()
		os.Remove(gzTmpName)
	}()

	src, err := os.Open(outTmp.Name())
	if err != nil {
		return parsedDemo{}, err
	}
	gz := gzip.NewWriter(gzTmp)
	if _, err := io.Copy(gz, src); err != nil {
		src.Close()
		return parsedDemo{}, err
	}
	src.Close()

	if err := gz.Close(); err != nil {
		return parsedDemo{}, err
	}
	if err := gzTmp.Sync(); err != nil {
		return parsedDemo{}, err
	}
	if err := gzTmp.Close(); err != nil {
		return parsedDemo{}, err
	}

	fi, err := os.Stat(gzTmpName)
	if err != nil {
		return parsedDemo{}, err
	}
	sizeBytes := fi.Size()

	if err := os.Rename(gzTmpName, destPath); err != nil {
		return parsedDemo{}, err
	}

	log.Printf("[parse-url] Demo saved: %s (%d bytes gzipped) from %s", id, sizeBytes, dem.name)

	return parsedDemo{
		ID:        id,
		SizeBytes: sizeBytes,
		DemName:   dem.name,
	}, nil
}

// detectFormat reads magic bytes to determine file format.
func detectFormat(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if err != nil || n < 3 {
		return "dem", nil
	}

	if buf[0] == rarMagic[0] && buf[1] == rarMagic[1] && buf[2] == rarMagic[2] {
		return "rar", nil
	}
	if n >= 4 && buf[0] == zstdMagic[0] && buf[1] == zstdMagic[1] && buf[2] == zstdMagic[2] && buf[3] == zstdMagic[3] {
		return "zstd", nil
	}

	return "dem", nil
}

// extractAllDemsFromRAR opens a RAR archive and extracts ALL .dem files.
// Path traversal safe: ignores archive entry paths, writes to system temp.
func extractAllDemsFromRAR(archivePath string) ([]extractedDem, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	rr, err := rardecode.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("cannot open RAR: %w", err)
	}

	var dems []extractedDem
	var totalDecompressed int64

	for {
		header, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			for _, d := range dems {
				os.Remove(d.path)
			}
			return nil, fmt.Errorf("RAR read error: %w", err)
		}

		name := header.Name
		if !strings.HasSuffix(strings.ToLower(name), ".dem") {
			continue
		}

		remaining := maxDecompressedBytes - totalDecompressed
		if remaining <= 0 {
			log.Printf("[parse-url] Total decompression limit reached, skipping remaining files")
			break
		}

		demTmp, err := os.CreateTemp("", "demo-extracted-*.dem")
		if err != nil {
			for _, d := range dems {
				os.Remove(d.path)
			}
			return nil, err
		}

		limited := io.LimitReader(rr, remaining)
		written, err := io.Copy(demTmp, limited)
		demTmp.Close()

		if err != nil {
			os.Remove(demTmp.Name())
			for _, d := range dems {
				os.Remove(d.path)
			}
			return nil, fmt.Errorf("extraction write failed: %w", err)
		}

		totalDecompressed += written
		baseName := filepath.Base(name)
		log.Printf("[parse-url] Extracted %s (%d bytes) from RAR", baseName, written)

		dems = append(dems, extractedDem{
			path: demTmp.Name(),
			name: baseName,
		})
	}

	if len(dems) == 0 {
		return nil, fmt.Errorf("no .dem file found in RAR archive")
	}

	return dems, nil
}

// validateDemoURL checks that the URL is a safe HLTV demo download link.
// SSRF protection: only *.hltv.org HTTPS URLs are allowed.
func validateDemoURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL is required")
	}

	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}

	if u.Scheme != "https" {
		return fmt.Errorf("only HTTPS URLs are allowed")
	}

	host := strings.ToLower(u.Hostname())
	if host != "hltv.org" && !strings.HasSuffix(host, ".hltv.org") {
		return fmt.Errorf("only *.hltv.org URLs are allowed")
	}

	return nil
}
