package main

import (
	"compress/bzip2"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// Compressed/decompressed size limits — anti-abuse / anti-bomb.
const (
	maxBz2CompressedBytes   int64 = 300 << 20  // 300 MB max bz2 download
	maxBz2DecompressedBytes int64 = 500 << 20  // 500 MB max after bunzip
	bz2DownloadTimeout            = 60 * time.Second
)

// downloadClient: no redirects, bounded timeout.
var downloadClient = &http.Client{
	Timeout: bz2DownloadTimeout,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// bz2DownloadResult bundles the outputs of a bz2 download+decompress pass.
type bz2DownloadResult struct {
	tmpPath          string // caller must os.Remove on success or failure
	sha256Hex        string
	decompressedSize int64
	// recordedAt is the ISO-8601 UTC timestamp of the Valve CDN `Last-Modified`
	// header (≈ match end time). Empty string if absent/unparseable.
	recordedAt string
}

// downloadAndDecompressBz2 downloads a .dem.bz2 from the given URL, streams it
// through bzip2.NewReader into a temp .dem file, computes SHA-256 on the
// decompressed bytes on-the-fly, and captures the CDN Last-Modified header.
func downloadAndDecompressBz2(url string) (*bz2DownloadResult, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()

	// Propagate 404 separately (Valve CDN expires demos after ~30 days)
	if resp.StatusCode == http.StatusNotFound {
		return nil, &notFoundError{}
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}

	// Parse Last-Modified (HTTP date, RFC 1123) → ISO 8601 UTC
	recordedAt := ""
	if lm := resp.Header.Get("Last-Modified"); lm != "" {
		if t, err := http.ParseTime(lm); err == nil {
			recordedAt = t.UTC().Format(time.RFC3339)
		}
	}

	// Cap the compressed stream
	limited := io.LimitReader(resp.Body, maxBz2CompressedBytes+1)
	bz := bzip2.NewReader(limited)

	// Write decompressed bytes to temp file + hash in parallel via TeeReader
	hasher := sha256.New()
	tee := io.TeeReader(bz, hasher)

	tmp, err := os.CreateTemp("", "steam-dem-*.dem")
	if err != nil {
		return nil, fmt.Errorf("create temp: %w", err)
	}
	tmpPath := tmp.Name()

	// Cap the decompressed output
	written, err := io.Copy(tmp, io.LimitReader(tee, maxBz2DecompressedBytes+1))
	tmp.Close()
	if err != nil {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("copy/bunzip: %w", err)
	}
	if written > maxBz2DecompressedBytes {
		os.Remove(tmpPath)
		return nil, fmt.Errorf("decompressed size exceeds %d bytes", maxBz2DecompressedBytes)
	}

	return &bz2DownloadResult{
		tmpPath:          tmpPath,
		sha256Hex:        hex.EncodeToString(hasher.Sum(nil)),
		decompressedSize: written,
		recordedAt:       recordedAt,
	}, nil
}

// notFoundError is returned when the Valve CDN responds with 404
// (demo expired — Valve retains demos for ~30 days).
type notFoundError struct{}

func (*notFoundError) Error() string { return "demo not found (404)" }

// isDemoNotFound detects the notFoundError sentinel.
func isDemoNotFound(err error) bool {
	_, ok := err.(*notFoundError)
	return ok
}
