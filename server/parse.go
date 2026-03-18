package main

import (
	"compress/gzip"
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"

	"github.com/klauspost/compress/zstd"
)

func handleParse(w http.ResponseWriter, r *http.Request) {
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

	// --- Phase 1: Buffer body to disk BEFORE acquiring semaphore ---
	// This allows multiple uploads to transfer simultaneously over the network
	// while only one parser runs at a time (RAM safety).

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	var bodyReader io.Reader = r.Body
	if r.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(r.Body)
		if err != nil {
			http.Error(w, "Invalid gzip", http.StatusBadRequest)
			return
		}
		defer gz.Close()
		bodyReader = gz
	}

	tmpFile, err := os.CreateTemp("", "demo-*.dem")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, bodyReader); err != nil {
		http.Error(w, "File too large or upload failed", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body.Close()

	// Detect zstd format (Faceit .dem.zst) via magic bytes and decompress
	parserInput := tmpFile.Name()
	if isZstd, _ := hasZstdMagic(tmpFile.Name()); isZstd {
		decompFile, err := decompressZstd(tmpFile.Name())
		if err != nil {
			http.Error(w, "Failed to decompress zstd", http.StatusBadRequest)
			return
		}
		defer os.Remove(decompFile)
		parserInput = decompFile
		log.Printf("Decompressed zstd demo to %s", decompFile)
	}

	// --- Phase 2: Wait in queue for parser slot (max queueTimeout) ---

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

	// --- Phase 3: Run parser ---

	cmd := exec.Command("./parser", parserInput)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, stdout)

	stderrBytes, _ := io.ReadAll(stderr)

	if err := cmd.Wait(); err != nil {
		log.Printf("Parser error: %v | stderr: %s", err, string(stderrBytes))
	}
}

// zstd magic bytes: 0x28 0xB5 0x2F 0xFD
var zstdMagic = []byte{0x28, 0xB5, 0x2F, 0xFD}

func hasZstdMagic(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	buf := make([]byte, 4)
	n, err := f.Read(buf)
	if err != nil || n < 4 {
		return false, err
	}
	for i := 0; i < 4; i++ {
		if buf[i] != zstdMagic[i] {
			return false, nil
		}
	}
	return true, nil
}

func decompressZstd(srcPath string) (string, error) {
	src, err := os.Open(srcPath)
	if err != nil {
		return "", err
	}
	defer src.Close()

	decoder, err := zstd.NewReader(src)
	if err != nil {
		return "", err
	}
	defer decoder.Close()

	dst, err := os.CreateTemp("", "demo-decompressed-*.dem")
	if err != nil {
		return "", err
	}
	dstPath := dst.Name()

	if _, err := io.Copy(dst, decoder); err != nil {
		dst.Close()
		os.Remove(dstPath)
		return "", err
	}
	dst.Close()
	return dstPath, nil
}
