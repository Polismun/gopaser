package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"sync"

	"cloud.google.com/go/firestore"
	firebase "firebase.google.com/go/v4"
	"google.golang.org/api/option"
)

var (
	firestoreOnce   sync.Once
	firestoreClient *firestore.Client
)

// getFirestoreClient returns a singleton Firestore client.
// Requires FIREBASE_SERVICE_ACCOUNT env var (JSON string) or GOOGLE_APPLICATION_CREDENTIALS file path.
// Returns nil if not configured (graceful degradation).
func getFirestoreClient() *firestore.Client {
	firestoreOnce.Do(func() {
		ctx := context.Background()

		var app *firebase.App
		var err error

		// Try FIREBASE_SERVICE_ACCOUNT env var first (JSON string, same as Vercel)
		saJSON := os.Getenv("FIREBASE_SERVICE_ACCOUNT")
		if saJSON != "" {
			// Parse to extract project_id
			var sa struct {
				ProjectID string `json:"project_id"`
			}
			if jsonErr := json.Unmarshal([]byte(saJSON), &sa); jsonErr != nil {
				log.Printf("[firestore] Failed to parse FIREBASE_SERVICE_ACCOUNT: %v", jsonErr)
				return
			}

			conf := &firebase.Config{ProjectID: sa.ProjectID}
			app, err = firebase.NewApp(ctx, conf, option.WithCredentialsJSON([]byte(saJSON)))
		} else {
			// Fallback to GOOGLE_APPLICATION_CREDENTIALS file
			app, err = firebase.NewApp(ctx, nil)
		}

		if err != nil {
			log.Printf("[firestore] Firebase init failed: %v", err)
			return
		}

		firestoreClient, err = app.Firestore(ctx)
		if err != nil {
			log.Printf("[firestore] Firestore client failed: %v", err)
			firestoreClient = nil
			return
		}

		log.Println("[firestore] Firestore client initialized")
	})

	return firestoreClient
}
