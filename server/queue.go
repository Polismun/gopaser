package main

import (
	"encoding/json"
	"net/http"
)

type queueStatus struct {
	Parsing int `json:"parsing"`
	Waiting int `json:"waiting"`
}

func handleQueue(w http.ResponseWriter, r *http.Request) {
	setCORS(w)

	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	parsing := len(parseSem)
	waiting := int(queueWaiting.Load())

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(queueStatus{
		Parsing: parsing,
		Waiting: waiting,
	})
}
