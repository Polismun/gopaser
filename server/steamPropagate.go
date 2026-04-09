package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
)

// POST /steam/propagate-new-user — propagate existing matches to a newly linked user.
// Called fire-and-forget from Vercel Steam link callback or manually by admin.
func handlePropagateNewUser(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	if verifyURL != "" {
		if status, err := verifyAuthAt(authHeader, "/api/verify-steam-sync"); status != http.StatusOK {
			msg := "Unauthorized"
			if err != nil {
				msg = err.Error()
			}
			http.Error(w, msg, status)
			return
		}
	}

	var req struct {
		UID     string `json:"uid"`
		SteamID string `json:"steamId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.UID == "" || req.SteamID == "" {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	fs := getFirestoreClient()
	if fs == nil {
		http.Error(w, "Firestore not configured", http.StatusServiceUnavailable)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	propagated, err := propagateForNewUser(ctx, fs, req.UID, req.SteamID)
	if err != nil {
		log.Printf("[steam-propagate] new user error: %v", err)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"propagated": propagated})
}

// findUsersBySteamIds queries users whose steamLink.steamId is in the given list.
// Returns a map of steamId64 → uid. Excludes the given excludeUID.
func findUsersBySteamIds(ctx context.Context, fs *firestore.Client, steamIds []string, excludeUID string) map[string]string {
	if len(steamIds) == 0 {
		return nil
	}

	iter := fs.Collection("users").
		Where("steamLink.steamId", "in", steamIds).
		Documents(ctx)
	defer iter.Stop()

	result := make(map[string]string)
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			break
		}
		uid := doc.Ref.ID
		if uid == excludeUID {
			continue
		}
		data := doc.Data()
		sl, ok := data["steamLink"].(map[string]interface{})
		if !ok {
			continue
		}
		sid, _ := sl["steamId"].(string)
		if sid != "" {
			result[sid] = uid
		}
	}
	return result
}

// propagateToCoPlayers adds other site users as participants to the match
// and creates DemoDocs for them. Called after a successful parse.
func propagateToCoPlayers(ctx context.Context, fs *firestore.Client, matchDocID string, match MatchDoc, demoHash, vpsFileID, recordedAt string) {
	if match.Status != "parsed" || vpsFileID == "" || demoHash == "" {
		return
	}

	// Collect steamId64s from the 10 players
	var steamIds []string
	for _, p := range match.Players {
		if p.AccountID > 0 {
			steamIds = append(steamIds, accountIDToSteamID64(p.AccountID))
		}
	}
	if len(steamIds) == 0 {
		return
	}

	// Find other site users in this match (exclude first participant)
	excludeUID := ""
	if len(match.ParticipantUids) > 0 {
		excludeUID = match.ParticipantUids[0]
	}
	coPlayers := findUsersBySteamIds(ctx, fs, steamIds, excludeUID)
	if len(coPlayers) == 0 {
		return
	}

	for _, uid := range coPlayers {
		// Add as participant to the shared MatchDoc
		if err := addParticipant(ctx, fs, matchDocID, uid); err != nil {
			log.Printf("[steam-propagate] failed to add participant %s: %v", uid, err)
			continue
		}

		// Create DemoDoc for this user (dedup — same vpsFileId, no re-download)
		handleDedupHitGo(ctx, fs, uid, match.Sharecode, demoHash, vpsFileID, recordedAt, "", false)
		log.Printf("[steam-propagate] added user %s to match %s", uid, matchDocID)
	}
}

// propagateForNewUser finds all parsed matches where the new user is a player,
// adds them as participant, and creates DemoDocs. Called on first sync.
func propagateForNewUser(ctx context.Context, fs *firestore.Client, uid, steamId64 string) (int, error) {
	accountId := steamID64ToAccountID(steamId64)
	if accountId == 0 {
		return 0, fmt.Errorf("invalid steamId64: %s", steamId64)
	}

	// Query matches where this accountId is in playerAccountIds
	iter := fs.Collection("matches").
		Where("status", "==", "parsed").
		Where("playerAccountIds", "array-contains", accountId).
		Documents(ctx)
	defer iter.Stop()

	propagated := 0
	for {
		doc, err := iter.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			log.Printf("[steam-propagate] query error for new user %s: %v", uid, err)
			break
		}

		matchDocID := doc.Ref.ID

		// Check if user is already a participant
		if uids, ok := doc.Data()["participantUids"].([]interface{}); ok {
			already := false
			for _, u := range uids {
				if fmt.Sprintf("%v", u) == uid {
					already = true
					break
				}
			}
			if already {
				continue
			}
		}

		// Add as participant
		if err := addParticipant(ctx, fs, matchDocID, uid); err != nil {
			log.Printf("[steam-propagate] failed to add new user %s to match %s: %v", uid, matchDocID, err)
			continue
		}

		// Create DemoDoc
		demoFileId, _ := doc.Data()["demoFileId"].(string)
		sharecode, _ := doc.Data()["sharecode"].(string)
		if demoFileId == "" || sharecode == "" {
			propagated++
			continue
		}

		demoIter := fs.Collection("demos").Where("vpsFileId", "==", demoFileId).Limit(1).Documents(ctx)
		if demoSnap, err := demoIter.Next(); err == nil {
			hash, _ := demoSnap.Data()["demoHash"].(string)
			recordedAt, _ := demoSnap.Data()["recordedAt"].(string)
			if hash != "" {
				handleDedupHitGo(ctx, fs, uid, sharecode, hash, demoFileId, recordedAt, "", false)
			}
		}
		demoIter.Stop()
		propagated++
	}

	if propagated > 0 {
		log.Printf("[steam-propagate] added user %s to %d existing matches", uid, propagated)
	}
	return propagated, nil
}
