package main

import (
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

func handleParse(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Spawn the parser binary, pipe request body to stdin
	cmd := exec.Command("./parser")
	cmd.Stdin = r.Body
	defer r.Body.Close()

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

	// Stream parser JSON output directly to the HTTP response
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	io.Copy(w, stdout)

	stderrBytes, _ := io.ReadAll(stderr)

	if err := cmd.Wait(); err != nil {
		log.Printf("Parser error: %v | stderr: %s", err, string(stderrBytes))
	}
}
