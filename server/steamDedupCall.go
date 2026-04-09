package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"cloud.google.com/go/firestore"
)

// checkDemoHashResponse mirrors the Vercel /api/demo-hash-check response shape
// for the single-hash mode. `exists=false` means the hash is new.
type checkDemoHashResponse struct {
	Exists    bool   `json:"exists"`
	VpsFileId string `json:"vpsFileId,omitempty"`
}

// checkDemoHashViaVercel posts {hash} to the Vercel /api/demo-hash-check endpoint
// and returns (exists, existingVpsFileId). The authHeader is forwarded so Vercel
// can authenticate the caller (the same Firebase Bearer used by the VPS handler).
func checkDemoHashViaVercel(hash, authHeader string) (bool, string, error) {
	if verifyURL == "" {
		return false, "", nil // DEV_MODE only — treat all as new
	}

	body, err := json.Marshal(map[string]string{"hash": hash})
	if err != nil {
		return false, "", err
	}

	req, err := http.NewRequest("POST", verifyURL+"/api/demo-hash-check", bytes.NewReader(body))
	if err != nil {
		return false, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := verifyClient.Do(req)
	if err != nil {
		return false, "", fmt.Errorf("hash check unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return false, "", fmt.Errorf("hash check HTTP %d", resp.StatusCode)
	}

	var out checkDemoHashResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, "", fmt.Errorf("hash check decode: %w", err)
	}
	return out.Exists, out.VpsFileId, nil
}

// checkDemoHashLocal queries Firestore demoHashes/{hash} directly.
// Used by cron sync when no user idToken is available.
func checkDemoHashLocal(ctx context.Context, fs *firestore.Client, hash string) (bool, string, error) {
	snap, err := fs.Collection("demoHashes").Doc(hash).Get(ctx)
	if err != nil {
		// NotFound → hash is new
		return false, "", nil
	}
	data := snap.Data()
	vpsFileId, _ := data["vpsFileId"].(string)
	return true, vpsFileId, nil
}
