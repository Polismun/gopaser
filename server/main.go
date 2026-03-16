package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/parse", handleParse)
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	log.Printf("Parser server listening on :%s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}

func setCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
}

// verifyAuth forwards the Authorization header to Vercel for validation.
// If VERIFY_URL is not set (dev local), auth is skipped.
func verifyAuth(authHeader string) (int, error) {
	verifyURL := os.Getenv("VERIFY_URL")
	if verifyURL == "" {
		return http.StatusOK, nil
	}

	req, err := http.NewRequest("POST", verifyURL+"/api/verify-upload", nil)
	if err != nil {
		return http.StatusInternalServerError, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return http.StatusBadGateway, fmt.Errorf("auth service unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var body struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&body)
		return resp.StatusCode, fmt.Errorf("%s", body.Error)
	}

	return http.StatusOK, nil
}

func handleParse(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	// Handle CORS preflight
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Verify auth via Vercel (skipped if VERIFY_URL not set)
	status, err := verifyAuth(r.Header.Get("Authorization"))
	if status != http.StatusOK {
		errMsg := "Unauthorized"
		if err != nil {
			errMsg = err.Error()
		}
		http.Error(w, errMsg, status)
		return
	}

	// Buffer body to a temp file (parser needs seekable input)
	tmpFile, err := os.CreateTemp("", "demo-*.dem")
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create temp file: %v", err), http.StatusInternalServerError)
		return
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	if _, err := io.Copy(tmpFile, r.Body); err != nil {
		http.Error(w, fmt.Sprintf("failed to buffer demo: %v", err), http.StatusInternalServerError)
		return
	}
	r.Body.Close()

	// Spawn the parser binary with the temp file path
	cmd := exec.Command("./parser", tmpFile.Name())

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		http.Error(w, fmt.Sprintf("stdout pipe error: %v", err), http.StatusInternalServerError)
		return
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		http.Error(w, fmt.Sprintf("stderr pipe error: %v", err), http.StatusInternalServerError)
		return
	}

	if err := cmd.Start(); err != nil {
		http.Error(w, fmt.Sprintf("failed to start parser: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, stdout)

	stderrBytes, _ := io.ReadAll(stderr)

	if err := cmd.Wait(); err != nil {
		log.Printf("Parser error: %v | stderr: %s", err, string(stderrBytes))
	}
}
