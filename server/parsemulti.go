package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nwaples/rardecode/v2"
)

// RAR magic bytes: 0x52 0x61 0x72
var rarMagic = []byte{0x52, 0x61, 0x72}

type extractedDem struct {
	path string
	name string // original filename (e.g. "navi-vs-faze-de_mirage.dem")
}

type parsedDemo struct {
	ID        string `json:"id"`
	SizeBytes int64  `json:"sizeBytes"`
	DemName   string `json:"demName"`
}

// POST /parse-multi — accept uploaded .rar/.dem/.zst archive, extract all .dem, parse each, save each.
// Returns { demos: [{ id, sizeBytes, demName }, ...] }.
// Used by admin to upload HLTV .rar archives containing multiple match demos.
func handleParseMulti(w http.ResponseWriter, r *http.Request) {
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

	// --- Phase 1: Buffer upload to disk BEFORE semaphore ---

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

	tmpFile, err := os.CreateTemp("", "multi-upload-*")
	if err != nil {
		http.Error(w, "Internal error", http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpFile.Name())

	if _, err := io.Copy(tmpFile, bodyReader); err != nil {
		tmpFile.Close()
		http.Error(w, "File too large or upload failed", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body.Close()
	tmpFile.Close()

	// Detect format and extract .dem files
	dems, err := extractDems(tmpFile.Name())
	if err != nil {
		log.Printf("[parse-multi] extract failed: %v", err)
		http.Error(w, fmt.Sprintf("Extract failed: %v", err), http.StatusBadRequest)
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

	// --- Phase 3: Parse each .dem + save ---

	var results []parsedDemo
	for _, dem := range dems {
		result, err := parseAndSave(dem)
		if err != nil {
			log.Printf("[parse-multi] failed to parse %s: %v", dem.name, err)
			continue
		}
		results = append(results, result)
		os.Remove(dem.path)
	}

	if len(results) == 0 {
		http.Error(w, "All demos failed to parse", http.StatusInternalServerError)
		return
	}

	log.Printf("[parse-multi] Parsed %d/%d demos", len(results), len(dems))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"demos": results,
	})
}

// extractDems detects the file format and returns extracted .dem file(s).
func extractDems(path string) ([]extractedDem, error) {
	format, err := detectFormat(path)
	if err != nil {
		return nil, err
	}

	switch format {
	case "rar":
		return extractAllDemsFromRAR(path)
	case "zstd":
		demPath, err := decompressZstd(path)
		if err != nil {
			return nil, err
		}
		return []extractedDem{{path: demPath, name: "demo.dem"}}, nil
	default:
		// Raw .dem — copy so caller can safely os.Remove
		dst, err := os.CreateTemp("", "dem-copy-*.dem")
		if err != nil {
			return nil, err
		}
		src, err := os.Open(path)
		if err != nil {
			dst.Close()
			os.Remove(dst.Name())
			return nil, err
		}
		_, err = io.Copy(dst, src)
		src.Close()
		dst.Close()
		if err != nil {
			os.Remove(dst.Name())
			return nil, err
		}
		return []extractedDem{{path: dst.Name(), name: "demo.dem"}}, nil
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

	log.Printf("[parse-multi] Demo saved: %s (%d bytes gzipped) from %s", id, sizeBytes, dem.name)

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
			log.Printf("[parse-multi] Total decompression limit reached")
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
		log.Printf("[parse-multi] Extracted %s (%d bytes) from RAR", baseName, written)

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
