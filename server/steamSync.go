package main

import (
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"sync"

	"cs2-parser-server/stats"

	"cloud.google.com/go/firestore"
)

const (
	maxSharecodesPerSync = 20
	maxFailedSharecodes  = 20
	gcBotSyncTimeout     = 20 * time.Second
)

// Per-user sync mutex — prevents concurrent syncs for the same uid.
var activeSyncs sync.Map

// Retryable error codes — sharecode persisted for next sync.
var retryableCodes = map[string]bool{
	"VPS_ERROR": true, "NETWORK": true, "STEAM_API_UNAVAILABLE": true, "UNKNOWN": true,
	"DOWNLOAD_CORRUPT": true, // often transient (network cut mid-stream), retry up to maxRetries
}

// Max retries per sharecode before marking as permanently failed.
const maxRetriesPerSharecode = 10

type syncRequest struct {
	UID     string `json:"uid"`
	IDToken string `json:"idToken"`
}

type syncResponse struct {
	Discovered int            `json:"discovered"`
	Imported   int            `json:"imported"`
	Errors     []syncErrEntry `json:"errors"`
}

type syncErrEntry struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// POST /steam/sync — orchestrates the full Steam sync for a user.
// Called by Vercel proxy (fire-and-forget). No timeout.
func handleSteamSync(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	// Auth: verify via Vercel
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" && verifyURL != "" {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
		return
	}
	status, err := verifyAuthAt(authHeader, "/api/verify-steam-sync")
	if status != http.StatusOK {
		msg := "Unauthorized"
		if err != nil {
			msg = err.Error()
		}
		http.Error(w, msg, status)
		return
	}

	var req syncRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// Per-user mutex: reject if a sync is already running for this uid.
	if _, loaded := activeSyncs.LoadOrStore(req.UID, true); loaded {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]interface{}{"already_syncing": true})
		return
	}
	defer activeSyncs.Delete(req.UID)

	fs := getFirestoreClient()
	if fs == nil {
		http.Error(w, "Firestore not configured", http.StatusServiceUnavailable)
		return
	}

	// Use a detached context — the HTTP client (Vercel proxy) may close the
	// connection before the sync finishes, but we must keep writing to Firestore.
	// 30 min allows for 20 matches × ~60s each (download + parse + stats + writes).
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	result := runSync(ctx, fs, req.UID, req.IDToken)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func runSync(ctx context.Context, fs *firestore.Client, uid, idToken string) syncResponse {
	var discovered []string
	var imported int
	var errors []syncErrEntry

	// Read steamLink
	sl, err := readSteamLink(ctx, fs, uid)
	if err != nil {
		return syncResponse{Errors: []syncErrEntry{{Code: "NO_CREDENTIALS", Detail: err.Error()}}}
	}
	if sl.SteamID == "" || sl.AuthCodeEncrypted == "" || sl.AuthCodeIV == "" || sl.LatestSharecode == "" {
		return syncResponse{Errors: []syncErrEntry{{Code: "NO_CREDENTIALS", Detail: "missing steamLink fields"}}}
	}

	// First sync ever: propagate existing matches from co-players
	if sl.LastSyncAt == "" && sl.SteamID != "" {
		propagated, _ := propagateForNewUser(ctx, fs, uid, sl.SteamID)
		if propagated > 0 {
			imported += propagated
		}
	}

	// Decrypt auth code
	authCode, err := decryptAuthCode(sl.AuthCodeEncrypted, sl.AuthCodeIV)
	if err != nil {
		return syncResponse{Errors: []syncErrEntry{{Code: "UNKNOWN", Detail: "decrypt: " + err.Error()}}}
	}

	// Phase 1: discover new sharecodes
	cursor := sl.LatestSharecode
	hardStop := false
	for i := 0; i < maxSharecodesPerSync && !hardStop; i++ {
		next, err := getNextMatchSharingCode(sl.SteamID, authCode, cursor)
		if err != nil {
			code := "UNKNOWN"
			detail := err.Error()
			if strings.Contains(detail, "INVALID_AUTH_CODE") {
				code = "INVALID_AUTH_CODE"
				hardStop = true
			} else if strings.Contains(detail, "STEAM_API_429") {
				code = "STEAM_API_429"
				hardStop = true
			}
			log.Printf("[steam-sync] discovery error: %s: %s", code, detail)
			errors = append(errors, syncErrEntry{Code: code, Detail: detail})
			if !hardStop {
				break
			}
			continue
		}
		if next == "" {
			break
		}
		discovered = append(discovered, next)
		cursor = next
	}

	// Phase 2: process — retry previous failures first, then new (dedup)
	seen := make(map[string]bool, len(sl.FailedSharecodes)+len(discovered))
	var toProcess []string
	for _, c := range append(sl.FailedSharecodes, discovered...) {
		if !seen[c] {
			seen[c] = true
			toProcess = append(toProcess, c)
		}
	}
	var newFailed []string
	retries := sl.FailedRetries
	if retries == nil {
		retries = make(map[string]int)
	}
	newRetries := make(map[string]int)

	for i, code := range toProcess {
		if i > 0 {
			time.Sleep(3 * time.Second)
		}
		err := processSharecode(ctx, fs, uid, idToken, code)
		if err != nil {
			errCode := "UNKNOWN"
			if strings.Contains(err.Error(), ":") {
				errCode = strings.SplitN(err.Error(), ":", 2)[0]
			}
			attempts := retries[code] + 1
			log.Printf("[steam-sync] sharecode %s failed (attempt %d): %s: %s", code, attempts, errCode, err.Error())
			errors = append(errors, syncErrEntry{Code: errCode, Detail: err.Error()})

			if retryableCodes[errCode] && attempts < maxRetriesPerSharecode && len(newFailed) < maxFailedSharecodes {
				newFailed = append(newFailed, code)
				newRetries[code] = attempts
			} else {
				// Max retries exceeded or non-retryable: mark MatchDoc as failed
				if ex, _, st, mid := matchExistsBySharecode(ctx, fs, uid, code); ex && (st == "pending" || st == "discovered") {
					reason := errCode
					if attempts >= maxRetriesPerSharecode {
						reason = errCode + "_MAX_RETRIES"
					}
					if fErr := updateMatchDocFailed(ctx, fs, mid, reason); fErr != nil {
						log.Printf("[steam-sync] failed to mark match %s as failed: %v", mid, fErr)
					}
				}
			}
		} else {
			imported++
		}
	}

	// Persist progress — use a fresh context so this write succeeds even if
	// the main sync context expired (otherwise failedSharecodes are lost).
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer saveCancel()

	updates := map[string]interface{}{
		"lastSyncAt":            nowISO(),
		"lastSyncImportedCount": imported,
		"failedSharecodes":      newFailed,
		"failedRetries":         newRetries,
	}
	if cursor != sl.LatestSharecode {
		updates["latestSharecode"] = cursor
	}
	if len(errors) > 0 {
		updates["lastSyncError"] = errors[0].Code
	} else {
		updates["lastSyncError"] = nil
	}
	if err := updateSteamLink(saveCtx, fs, uid, updates); err != nil {
		log.Printf("[steam-sync] failed to update steamLink for %s: %v", uid, err)
	}

	log.Printf("[steam-sync] uid=%s discovered=%d imported=%d errors=%d", uid, len(discovered), imported, len(errors))
	return syncResponse{Discovered: len(discovered), Imported: imported, Errors: errors}
}

func processSharecode(ctx context.Context, fs *firestore.Client, uid, idToken, code string) error {
	// 0. Dedup: match by sharecode
	docExists, userIsParticipant, matchStatus, existingMatchID := matchExistsBySharecode(ctx, fs, uid, code)

	if docExists && userIsParticipant {
		if matchStatus == "parsed" || matchStatus == "failed" {
			return nil // already done for this user
		}
	}

	// Doc exists but user not yet a participant → just join the existing match
	if docExists && !userIsParticipant {
		if err := addParticipant(ctx, fs, existingMatchID, uid); err != nil {
			log.Printf("[steam-sync] failed to add participant %s to match %s: %v", uid, existingMatchID, err)
		} else {
			log.Printf("[steam-sync] added user %s as participant to existing match %s", uid, existingMatchID)
		}
		// Create DemoDoc for this user if match is parsed and has a demo
		if matchStatus == "parsed" {
			snap, _ := fs.Collection("matches").Doc(existingMatchID).Get(ctx)
			if snap != nil {
				demoFileId, _ := snap.Data()["demoFileId"].(string)
				if demoFileId != "" {
					// Find demoHash from an existing DemoDoc
					demoIter := fs.Collection("demos").Where("vpsFileId", "==", demoFileId).Limit(1).Documents(ctx)
					if demoSnap, err := demoIter.Next(); err == nil {
						hash, _ := demoSnap.Data()["demoHash"].(string)
						recordedAt, _ := demoSnap.Data()["recordedAt"].(string)
						if hash != "" {
							handleDedupHitGo(ctx, fs, uid, code, hash, demoFileId, recordedAt, "", false)
						}
					}
					demoIter.Stop()
				}
			}
		}
		return nil
	}

	// If exists but stuck pending/discovered (no demo), we'll re-process from download step
	resumePending := docExists && userIsParticipant && (matchStatus == "pending" || matchStatus == "discovered")

	// Skip expired matches early — check matchDate from existing MatchDoc before GC call
	if resumePending {
		if snap, err := fs.Collection("matches").Doc(existingMatchID).Get(ctx); err == nil {
			if md, ok := snap.Data()["matchDate"].(string); ok {
				if t, err := time.Parse(time.RFC3339, md); err == nil {
					if time.Since(t) > 31*24*time.Hour {
						log.Printf("[steam-sync] skipping expired resumed match %s (%.0f days old)", code, time.Since(t).Hours()/24)
						return fmt.Errorf("EXPIRED_DEMO:match older than 31 days")
					}
				}
			}
		}
	}

	// Dedup: demo by sharecode (cross-user)
	demoHit, _ := findDemoBySharcode(ctx, fs, code)
	if demoHit != nil {
		if demoHit.OwnerID == uid {
			return nil
		}
		if demoHit.VpsFileID != "" && demoHit.DemoHash != "" {
			return handleDedupHitGo(ctx, fs, uid, code, demoHit.DemoHash, demoHit.VpsFileID, demoHit.RecordedAt, "", true)
		}
	}

	// 1. GC bot → scoreboard + demo URL
	decoded, err := decodeSharecode(code)
	if err != nil {
		return fmt.Errorf("UNKNOWN:%v", err)
	}

	gcResult, err := callGCBotInternal(decoded.MatchID, decoded.ReservationID, decoded.TVPort)
	if err != nil {
		return fmt.Errorf("VPS_ERROR:GC bot: %v", err)
	}

	// Resolve persona names
	steamIDs := make([]string, len(gcResult.Players))
	for i, p := range gcResult.Players {
		steamIDs[i] = accountIDToSteamID64(p.AccountID)
	}
	profileMap := fetchPlayerProfiles(steamIDs)

	// 2. Prepare MatchDoc pending (created after download succeeds to avoid phantom entries)
	var matchDocID string
	var pendingMatch *MatchDoc
	if resumePending {
		matchDocID = existingMatchID
		log.Printf("[steam-sync] resuming stuck pending match %s for sharecode %s", matchDocID, code)
	} else {
		matchDocID = gcResult.MatchID
		if matchDocID == "" {
			matchDocID = generateSyncUUID()
		}
		matchDate := nowISO()
		if gcResult.Matchtime > 0 {
			matchDate = time.Unix(int64(gcResult.Matchtime), 0).UTC().Format(time.RFC3339)
		}

		// Skip matches older than 31 days (Valve CDN expires demos ~14-30 days)
		if gcResult.Matchtime > 0 {
			age := time.Since(time.Unix(int64(gcResult.Matchtime), 0))
			if age > 31*24*time.Hour {
				log.Printf("[steam-sync] skipping expired match %s (%.0f days old)", code, age.Hours()/24)
				return fmt.Errorf("EXPIRED_DEMO:match older than 31 days")
			}
		}

		gcPlayers := make([]MatchPlayer, len(gcResult.Players))
		for i, p := range gcResult.Players {
			sid := accountIDToSteamID64(p.AccountID)
			team := "CT"
			if i >= 5 {
				team = "T"
			}
			profile := profileMap[sid]
			gcPlayers[i] = MatchPlayer{
				AccountID: p.AccountID,
				Name:      profile.Name,
				Avatar:    profile.Avatar,
				Team:      team,
				Kills:     p.Kills, Deaths: p.Deaths, Assists: p.Assists,
				Score: p.Score, MVPs: p.MVPs, Headshots: p.Headshots,
				RankID: p.RankID, RankChange: p.RankChange, RankType: p.RankType, Wins: p.Wins,
			}
		}

		// Build flat accountIds array for cross-user propagation queries
		accountIds := make([]int64, 0, len(gcPlayers))
		for _, p := range gcPlayers {
			if p.AccountID > 0 {
				accountIds = append(accountIds, p.AccountID)
			}
		}

		pendingMatch = &MatchDoc{
			ID:              matchDocID,
			ParticipantUids: []string{uid},
			Status:          "discovered",
			Source:          "steam",
			MatchDate:       matchDate,
			Sharecode:       code,
			GCMatchID:       gcResult.MatchID,
			DemoURL:         gcResult.URL,
			Map:             gcResult.Map,
			TeamScores: func() [2]int {
				if len(gcResult.TeamScores) >= 2 {
					return [2]int{gcResult.TeamScores[0], gcResult.TeamScores[1]}
				}
				return [2]int{}
			}(),
			Duration:         gcResult.Duration,
			Players:          gcPlayers,
			PlayerAccountIds: accountIds,
			CreatedAt:        nowISO(),
		}
		// Create discovered MatchDoc immediately (skeleton row in UI)
		if err := createMatchDoc(ctx, fs, *pendingMatch); err != nil {
			log.Printf("[steam-sync] failed to create discovered MatchDoc: %v", err)
		}
	}

	if gcResult.URL == "" {
		return fmt.Errorf("EXPIRED_DEMO:GC returned no demo URL")
	}

	// 3. Download + decompress + hash
	log.Printf("[steam-sync] downloading %s for sharecode %s", gcResult.URL, code)
	dl, err := downloadAndDecompressBz2(gcResult.URL)
	if err != nil {
		if isDemoNotFound(err) {
			return fmt.Errorf("EXPIRED_DEMO:Valve CDN 404")
		}
		if isCorruptDownload(err) {
			return fmt.Errorf("DOWNLOAD_CORRUPT:%v", err)
		}
		return fmt.Errorf("VPS_ERROR:download: %v", err)
	}
	defer os.Remove(dl.tmpPath)

	// Download succeeded — upgrade discovered → pending
	if pendingMatch != nil && matchDocID != "" {
		if _, err := fs.Collection("matches").Doc(matchDocID).Update(ctx, []firestore.Update{
			{Path: "status", Value: "pending"},
		}); err != nil {
			log.Printf("[steam-sync] failed to upgrade MatchDoc to pending: %v", err)
		}
	}

	// Hash dedup (Vercel if user token available, Firestore direct for cron)
	var hashExists bool
	var existingFileID string
	if idToken != "" {
		hashExists, existingFileID, _ = checkDemoHashViaVercel(dl.sha256Hex, idToken)
	} else {
		hashExists, existingFileID, _ = checkDemoHashLocal(ctx, fs, dl.sha256Hex)
	}
	if hashExists {
		return handleDedupHitGo(ctx, fs, uid, code, dl.sha256Hex, existingFileID, dl.recordedAt, matchDocID, false)
	}

	// 4. Parse
	queueWaiting.Add(1)
	parseCtx, cancel := context.WithTimeout(ctx, queueTimeout)
	select {
	case parseSem <- struct{}{}:
		queueWaiting.Add(-1)
	case <-parseCtx.Done():
		queueWaiting.Add(-1)
		cancel()
		return fmt.Errorf("VPS_ERROR:parse queue timeout")
	}
	cancel()

	dem := extractedDem{path: dl.tmpPath, name: "steam-demo.dem"}
	parsed, err := parseAndSave(dem)
	<-parseSem
	if err != nil {
		return fmt.Errorf("VPS_ERROR:parse: %v", err)
	}

	// 5. Load parsed JSON → compute stats
	parseResult, err := loadParsedJSON(parsed.ID)
	if err != nil {
		return fmt.Errorf("VPS_ERROR:load JSON: %v", err)
	}

	allStats := stats.ComputeAllPlayerStats(parseResult, parsed.ID)

	// 6. Build metadata from parse result
	teamCT, teamT := "", ""
	scoreCT, scoreT := 0, 0
	for i := len(parseResult.Ticks) - 1; i >= 0; i-- {
		t := &parseResult.Ticks[i]
		// Look for last tick with score info (same logic as extractDemoMetadata TS)
		// Scores are in the tick data but we need to check the raw JSON structure
		// For simplicity, use the GC scores (already in MatchDoc)
		_ = t
		break
	}
	// Use player teams to derive team names
	for _, s := range parseResult.Stats {
		if s.Team == "CT" && teamCT == "" {
			teamCT = "Team " + s.Name
		}
		if s.Team == "T" && teamT == "" {
			teamT = "Team " + s.Name
		}
	}
	// Use GC scores as source of truth
	if len(gcResult.TeamScores) >= 2 {
		scoreCT = gcResult.TeamScores[0]
		scoreT = gcResult.TeamScores[1]
	}

	// Build player enrichments (name + rank + hltvRating from stats)
	var enrichments []playerEnrichment
	for _, s := range parseResult.Stats {
		if s.Name == "" {
			continue
		}
		accountID := int64(0)
		if s.SteamID != "" {
			accountID = steamID64ToAccountID(s.SteamID)
		}
		pe := playerEnrichment{
			AccountID: accountID,
			Name:      s.Name,
			RankID:    s.Rank,
			RankType:  s.RankType,
			Wins:      s.Wins,
		}
		// Find computed HLTV rating
		for _, cp := range allStats.Players {
			if cp.Name == s.Name {
				pe.HLTVRating = cp.HLTVRating
				break
			}
		}
		enrichments = append(enrichments, pe)
	}

	// 7. Write everything to Firestore
	createdAt := nowISO()

	// DemoDoc
	demoData := map[string]interface{}{
		"id":             parsed.ID,
		"vpsFileId":      parsed.ID,
		"ownerId":        uid,
		"demoHash":       dl.sha256Hex,
		"source":         "steam-auto",
		"steamSharecode": code,
		"visibility":     "private",
		"createdAt":      createdAt,
		"mapName":        parseResult.MapName,
		"teamCT":         teamCT,
		"teamT":          teamT,
		"scoreCT":        scoreCT,
		"scoreT":         scoreT,
		"tickRate":       parseResult.TickRate,
		"totalRounds":    allStats.TotalRounds,
		"fileSizeBytes":  parsed.SizeBytes,
	}
	if dl.recordedAt != "" {
		demoData["recordedAt"] = dl.recordedAt
	}
	// Add players array (from parse stats — basic fields for DemoDoc)
	var demoPlayers []map[string]interface{}
	for _, s := range parseResult.Stats {
		p := map[string]interface{}{
			"name":    s.Name,
			"team":    s.Team,
			"kills":   s.Kills,
			"deaths":  s.Deaths,
			"assists": s.Assists,
		}
		if s.SteamID != "" {
			p["steamId"] = s.SteamID
		}
		if s.Rank != 0 {
			p["rank"] = s.Rank
		}
		if s.RankType != 0 {
			p["rankType"] = s.RankType
		}
		if s.Wins != 0 {
			p["wins"] = s.Wins
		}
		demoPlayers = append(demoPlayers, p)
	}
	demoData["players"] = demoPlayers

	// Build complete player metadata for demoHashes (GC data + parse + hltvRating + avatars)
	// This is the source of truth for cross-user propagation
	var hashPlayers []map[string]interface{}
	if pendingMatch != nil {
		for _, gp := range pendingMatch.Players {
			hp := map[string]interface{}{
				"accountId": gp.AccountID,
				"name":      gp.Name,
				"team":      gp.Team,
				"kills":     gp.Kills,
				"deaths":    gp.Deaths,
				"assists":   gp.Assists,
				"score":     gp.Score,
				"mvps":      gp.MVPs,
				"headshots": gp.Headshots,
			}
			if gp.Avatar != "" {
				hp["avatar"] = gp.Avatar
			}
			if gp.RankID != 0 {
				hp["rankId"] = gp.RankID
			}
			if gp.RankChange != 0 {
				hp["rankChange"] = gp.RankChange
			}
			if gp.RankType != 0 {
				hp["rankType"] = gp.RankType
			}
			if gp.Wins != 0 {
				hp["wins"] = gp.Wins
			}
			// Merge enriched rank + hltvRating from parse
			for _, e := range enrichments {
				if e.AccountID == gp.AccountID {
					if e.RankID != 0 {
						hp["rankId"] = e.RankID
					}
					if e.RankType != 0 {
						hp["rankType"] = e.RankType
					}
					if e.Wins != 0 {
						hp["wins"] = e.Wins
					}
					if e.HLTVRating != 0 {
						hp["hltvRating"] = e.HLTVRating
					}
					break
				}
			}
			// Also add steamId string for dedup lookups
			if gp.AccountID > 0 {
				hp["steamId"] = accountIDToSteamID64(gp.AccountID)
			}
			hashPlayers = append(hashPlayers, hp)
		}
	}

	if err := createDemoDoc(ctx, fs, parsed.ID, demoData); err != nil {
		log.Printf("[steam-sync] DemoDoc write failed: %v", err)
		return fmt.Errorf("VPS_ERROR:DemoDoc write: %v", err)
	}

	// demoStats
	if err := saveDemoStats(ctx, fs, parsed.ID, allStats); err != nil {
		log.Printf("[steam-sync] demoStats write failed: %v", err)
		// Non-fatal: match still works without detailed stats
	}

	// demoHash — complete metadata for cross-user propagation
	hashPlayersForStore := hashPlayers
	if len(hashPlayersForStore) == 0 {
		hashPlayersForStore = demoPlayers // fallback if pendingMatch was nil (resumePending)
	}
	hashData := map[string]interface{}{
		"vpsFileId":     parsed.ID,
		"mapName":       parseResult.MapName,
		"serverName":    parseResult.ServerName,
		"teamCT":        teamCT,
		"teamT":         teamT,
		"scoreCT":       scoreCT,
		"scoreT":        scoreT,
		"tickRate":      parseResult.TickRate,
		"totalRounds":   allStats.TotalRounds,
		"fileSizeBytes": parsed.SizeBytes,
		"duration":      gcResult.Duration,
		"players":       hashPlayersForStore,
	}
	if dl.recordedAt != "" {
		hashData["recordedAt"] = dl.recordedAt
	}
	if err := saveDemoHash(ctx, fs, dl.sha256Hex, hashData); err != nil {
		log.Printf("[steam-sync] demoHash write failed: %v", err)
	}

	// Enrich MatchDoc → parsed
	if err := enrichMatchDoc(ctx, fs, matchDocID, matchEnrichment{
		DemoFileID:        parsed.ID,
		DemoStatsID:       parsed.ID,
		TeamCT:            teamCT,
		TeamT:             teamT,
		MapName:           parseResult.MapName,
		ServerName:        parseResult.ServerName,
		PlayerEnrichments: enrichments,
	}); err != nil {
		log.Printf("[steam-sync] MatchDoc enrich failed: %v", err)
	}

	// Propagate to other site users in this match
	if pendingMatch != nil {
		pendingMatch.Status = "parsed"
		pendingMatch.DemoFileID = parsed.ID
		propagateToCoPlayers(ctx, fs, matchDocID, *pendingMatch, dl.sha256Hex, parsed.ID, dl.recordedAt)
	}

	return nil
}

func handleDedupHitGo(ctx context.Context, fs *firestore.Client, uid, code, hash, vpsFileID, recordedAt, matchDocID string, createMatch bool) error {
	// Check if user already has this vpsFileId
	iter := fs.Collection("demos").
		Where("ownerId", "==", uid).
		Where("vpsFileId", "==", vpsFileID).
		Limit(1).
		Documents(ctx)
	doc, _ := iter.Next()
	iter.Stop()
	if doc != nil {
		return nil
	}

	// Read cached metadata from demoHashes
	hashSnap, err := fs.Collection("demoHashes").Doc(hash).Get(ctx)
	if err != nil {
		return fmt.Errorf("VPS_ERROR:demoHashes entry missing")
	}
	meta := hashSnap.Data()

	// Create new DemoDoc referencing same vpsFileId
	newDemoID := generateSyncUUID()
	demoData := map[string]interface{}{
		"id":             newDemoID,
		"vpsFileId":      vpsFileID,
		"ownerId":        uid,
		"demoHash":       hash,
		"source":         "steam-auto",
		"steamSharecode": code,
		"visibility":     "private",
		"createdAt":      nowISO(),
	}
	// Copy metadata fields
	for _, k := range []string{"mapName", "teamCT", "teamT", "scoreCT", "scoreT", "tickRate", "totalRounds", "fileSizeBytes", "players"} {
		if v, ok := meta[k]; ok {
			demoData[k] = v
		}
	}
	if recordedAt != "" {
		demoData["recordedAt"] = recordedAt
	} else if ra, ok := meta["recordedAt"].(string); ok {
		demoData["recordedAt"] = ra
	}
	if err := createDemoDoc(ctx, fs, newDemoID, demoData); err != nil {
		log.Printf("[steam-sync] dedup DemoDoc write failed: %v", err)
	}

	// Enrich pending MatchDoc if exists
	if matchDocID != "" {
		players := extractPlayersFromMeta(meta)
		if err := enrichMatchDoc(ctx, fs, matchDocID, matchEnrichment{
			DemoFileID:        vpsFileID,
			DemoStatsID:       vpsFileID,
			TeamCT:            strVal(meta, "teamCT"),
			TeamT:             strVal(meta, "teamT"),
			MapName:           strVal(meta, "mapName"),
			PlayerEnrichments: players,
		}); err != nil {
			log.Printf("[steam-sync] dedup enrich failed: %v", err)
		}
	}

	// Create full MatchDoc for cross-user dedup
	if createMatch {
		matchPlayers := buildMatchPlayersFromMeta(meta)
		ra := recordedAt
		if ra == "" {
			if v, ok := meta["recordedAt"].(string); ok {
				ra = v
			}
		}
		if ra == "" {
			ra = nowISO()
		}
		scoreCT, scoreT := intVal(meta, "scoreCT"), intVal(meta, "scoreT")

		newMatchID := generateSyncUUID()
		// Build flat accountIds for propagation queries
		var dedupAccountIds []int64
		for _, p := range matchPlayers {
			if p.AccountID > 0 {
				dedupAccountIds = append(dedupAccountIds, p.AccountID)
			}
		}

		newMatch := MatchDoc{
			ID:               newMatchID,
			ParticipantUids:  []string{uid},
			Status:           "parsed",
			Source:           "steam",
			MatchDate:        ra,
			Sharecode:        code,
			Map:              strVal(meta, "mapName"),
			TeamScores:       [2]int{scoreCT, scoreT},
			Duration:         intVal(meta, "duration"),
			Players:          matchPlayers,
			PlayerAccountIds: dedupAccountIds,
			DemoFileID:       vpsFileID,
			DemoStatsID:      vpsFileID,
			TeamCT:           strVal(meta, "teamCT"),
			TeamT:            strVal(meta, "teamT"),
			ServerName:       strVal(meta, "serverName"),
			CreatedAt:        nowISO(),
		}
		if err := createMatchDoc(ctx, fs, newMatch); err != nil {
			log.Printf("[steam-sync] dedup MatchDoc create failed: %v", err)
		}
	}

	return nil
}

// ── Admin: backfill demoUrl for existing matches ──

func handleBackfillDemoUrls(w http.ResponseWriter, r *http.Request) {
	setCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Admin auth — localhost bypass (SSH access = already admin)
	isLocal := strings.HasPrefix(r.RemoteAddr, "127.0.0.1:") || strings.HasPrefix(r.RemoteAddr, "[::1]:")
	if !isLocal && verifyURL != "" {
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		status, err := verifyAuthAt(authHeader, "/api/verify-admin")
		if status != http.StatusOK {
			msg := "Forbidden"
			if err != nil {
				msg = err.Error()
			}
			http.Error(w, msg, status)
			return
		}
	}

	fs := getFirestoreClient()
	if fs == nil {
		http.Error(w, "Firestore unavailable", http.StatusInternalServerError)
		return
	}
	ctx := context.Background()

	// Query all steam matches, filter in Go (avoids composite index requirement)
	cutoff := time.Now().AddDate(0, 0, -31)
	iter := fs.Collection("matches").
		Where("source", "==", "steam").
		Documents(ctx)

	type backfillResult struct {
		ID      string `json:"id"`
		URL     string `json:"url,omitempty"`
		Error   string `json:"error,omitempty"`
		Skipped bool   `json:"skipped,omitempty"`
	}
	var results []backfillResult
	filled := 0

	for {
		doc, err := iter.Next()
		if err != nil {
			if err.Error() != "no more items in iterator" {
				log.Printf("[backfill] iterator error: %v", err)
			}
			break
		}
		data := doc.Data()
		// Skip if already has demoUrl
		if url, ok := data["demoUrl"].(string); ok && url != "" {
			continue
		}
		// Skip matches older than 31 days (URL would be expired)
		if md, ok := data["matchDate"].(string); ok {
			if t, err := time.Parse(time.RFC3339, md); err == nil && t.Before(cutoff) {
				continue
			}
		}
		// Need sharecode to call GC
		code, ok := data["sharecode"].(string)
		if !ok || code == "" {
			continue
		}

		docID := doc.Ref.ID

		decoded, err := decodeSharecode(code)
		if err != nil {
			results = append(results, backfillResult{ID: docID, Error: fmt.Sprintf("decode: %v", err)})
			continue
		}

		gcResult, err := callGCBotInternal(decoded.MatchID, decoded.ReservationID, decoded.TVPort)
		if err != nil {
			results = append(results, backfillResult{ID: docID, Error: fmt.Sprintf("gc: %v", err)})
			time.Sleep(3 * time.Second)
			continue
		}
		if gcResult.URL == "" {
			results = append(results, backfillResult{ID: docID, Error: "no URL from GC"})
			time.Sleep(3 * time.Second)
			continue
		}

		// Update Firestore
		_, err = fs.Collection("matches").Doc(docID).Update(ctx, []firestore.Update{
			{Path: "demoUrl", Value: gcResult.URL},
		})
		if err != nil {
			results = append(results, backfillResult{ID: docID, Error: fmt.Sprintf("update: %v", err)})
		} else {
			results = append(results, backfillResult{ID: docID, URL: gcResult.URL})
			filled++
		}

		time.Sleep(3 * time.Second)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"filled":  filled,
		"total":   len(results),
		"results": results,
	})
}

// ── GC bot internal call ──

type gcBotResult struct {
	URL        string        `json:"url"`
	Matchtime  int           `json:"matchtime"`
	MatchID    string        `json:"matchId"`
	Map        string        `json:"map"`
	Duration   int           `json:"duration"`
	TeamScores [2]int        `json:"teamScores"`
	Players    []gcBotPlayer `json:"players"`
}

type gcBotPlayer struct {
	AccountID  int64 `json:"accountId"`
	Kills      int   `json:"kills"`
	Deaths     int   `json:"deaths"`
	Assists    int   `json:"assists"`
	Score      int   `json:"score"`
	MVPs       int   `json:"mvps"`
	Headshots  int   `json:"headshots"`
	RankID     int   `json:"rankId"`
	RankChange int   `json:"rankChange"`
	RankType   int   `json:"rankType"`
	Wins       int   `json:"wins"`
}

func callGCBotInternal(matchID, reservationID uint64, tvPort uint16) (*gcBotResult, error) {
	url := fmt.Sprintf("%s/gc/demo-url?matchId=%d&outcomeId=%d&token=%d",
		gcBotURL, matchID, reservationID, tvPort)

	client := &http.Client{Timeout: gcBotSyncTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return nil, fmt.Errorf("GC bot unreachable: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GC bot HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result gcBotResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("GC bot decode: %w", err)
	}
	return &result, nil
}

// ── Load parsed JSON from disk ──

func loadParsedJSON(vpsFileID string) (*stats.ParseResult, error) {
	path := demoFilePath(vpsFileID)
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer gz.Close()

	var result stats.ParseResult
	if err := json.NewDecoder(gz).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ── Helpers ──

func generateSyncUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func extractPlayersFromMeta(meta map[string]interface{}) []playerEnrichment {
	players, ok := meta["players"].([]interface{})
	if !ok {
		return nil
	}
	var result []playerEnrichment
	for _, raw := range players {
		p, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		name := strVal(p, "name")
		if name == "" {
			continue
		}
		sid := strVal(p, "steamId")
		accountID := int64(0)
		if sid != "" {
			accountID = steamID64ToAccountID(sid)
		}
		result = append(result, playerEnrichment{
			AccountID: accountID,
			Name:      name,
			RankID:    intVal(p, "rank"),
			RankType:  intVal(p, "rankType"),
			Wins:      intVal(p, "wins"),
		})
	}
	return result
}

func buildMatchPlayersFromMeta(meta map[string]interface{}) []MatchPlayer {
	players, ok := meta["players"].([]interface{})
	if !ok {
		return nil
	}
	var result []MatchPlayer
	for _, raw := range players {
		p, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		// accountId can be stored directly or derived from steamId
		accountID := int64Val(p, "accountId")
		if accountID == 0 {
			if sid := strVal(p, "steamId"); sid != "" {
				accountID = steamID64ToAccountID(sid)
			}
		}
		mp := MatchPlayer{
			AccountID:  accountID,
			Name:       strVal(p, "name"),
			Avatar:     strVal(p, "avatar"),
			Team:       strVal(p, "team"),
			Kills:      intVal(p, "kills"),
			Deaths:     intVal(p, "deaths"),
			Assists:    intVal(p, "assists"),
			Score:      intVal(p, "score"),
			MVPs:       intVal(p, "mvps"),
			Headshots:  intVal(p, "headshots"),
			RankID:     intVal(p, "rankId"),
			RankChange: intVal(p, "rankChange"),
			RankType:   intVal(p, "rankType"),
			Wins:       intVal(p, "wins"),
			HLTVRating: floatVal(p, "hltvRating"),
		}
		// Fallback: old metadata stored rank as "rank" not "rankId"
		if mp.RankID == 0 {
			mp.RankID = intVal(p, "rank")
		}
		result = append(result, mp)
	}
	return result
}

func intVal(m map[string]interface{}, key string) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int64:
		return int(v)
	case int:
		return v
	}
	return 0
}

func int64Val(m map[string]interface{}, key string) int64 {
	switch v := m[key].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	}
	return 0
}

func floatVal(m map[string]interface{}, key string) float64 {
	switch v := m[key].(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	}
	return 0
}

// startSteamCron launches a background goroutine that syncs all Steam-linked users every 30 minutes.
func startSteamCron() {
	go func() {
		// Run once at startup (10s delay to let Firestore init)
		time.Sleep(10 * time.Second)
		cronSyncAll()

		ticker := time.NewTicker(30 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			cronSyncAll()
		}
	}()
	log.Println("[steam-cron] scheduled every 30 minutes (first run in 10s)")
}

// cronSyncAll queries all users with Steam credentials and runs sync for each.
func cronSyncAll() {
	fs := getFirestoreClient()
	if fs == nil {
		log.Println("[steam-cron] Firestore not configured, skipping")
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Minute)
	defer cancel()

	// Cleanup: delete discovered MatchDocs older than 31 days
	cleanupExpiredMatches(ctx, fs)

	// Query users with encrypted auth code (= credentials set)
	iter := fs.Collection("users").Where("steamLink.authCodeEncrypted", "!=", "").Documents(ctx)
	defer iter.Stop()

	var uids []string
	for {
		doc, err := iter.Next()
		if err != nil {
			break
		}
		uids = append(uids, doc.Ref.ID)
	}

	if len(uids) == 0 {
		log.Println("[steam-cron] no users with Steam credentials")
		return
	}

	log.Printf("[steam-cron] starting sync for %d user(s)", len(uids))

	for i, uid := range uids {
		if i > 0 {
			time.Sleep(5 * time.Second)
		}

		// Respect per-user mutex — skip if manual sync is running
		if _, loaded := activeSyncs.LoadOrStore(uid, true); loaded {
			log.Printf("[steam-cron] uid=%s already syncing, skipped", uid)
			continue
		}

		result := runSync(ctx, fs, uid, "")
		activeSyncs.Delete(uid)

		log.Printf("[steam-cron] uid=%s discovered=%d imported=%d errors=%d",
			uid, result.Discovered, result.Imported, len(result.Errors))
	}

	log.Println("[steam-cron] done")
}

// cleanupExpiredMatches deletes discovered and failed MatchDocs older than 31 days,
// and removes their sharecodes from the owner's failedSharecodes list.
func cleanupExpiredMatches(ctx context.Context, fs *firestore.Client) {
	cutoff := time.Now().Add(-31 * 24 * time.Hour).Format(time.RFC3339)

	// Collect expired sharecodes per owner for user doc cleanup
	expiredByOwner := make(map[string][]string)

	for _, status := range []string{"discovered", "failed"} {
		// Single-field query (no composite index needed), filter age in Go
		iter := fs.Collection("matches").
			Where("status", "==", status).
			Documents(ctx)

		deleted := 0
		for {
			doc, err := iter.Next()
			if err != nil {
				break
			}
			data := doc.Data()
			md, _ := data["matchDate"].(string)
			if md == "" {
				continue
			}
			if md >= cutoff {
				continue // not expired
			}
			sharecode, _ := data["sharecode"].(string)
			if sharecode == "" {
				continue
			}
			// Collect for all participants (new model) + legacy ownerId
			if uids, ok := data["participantUids"].([]interface{}); ok {
				for _, u := range uids {
					if uid := fmt.Sprintf("%v", u); uid != "" {
						expiredByOwner[uid] = append(expiredByOwner[uid], sharecode)
					}
				}
			} else if ownerID, ok := data["ownerId"].(string); ok && ownerID != "" {
				expiredByOwner[ownerID] = append(expiredByOwner[ownerID], sharecode)
			}
			if _, err := doc.Ref.Delete(ctx); err == nil {
				deleted++
			}
		}
		iter.Stop()
		if deleted > 0 {
			log.Printf("[steam-cron] cleaned up %d expired %s matches", deleted, status)
		}
	}

	// Remove expired sharecodes from each user's failedSharecodes
	for uid, codes := range expiredByOwner {
		expiredSet := make(map[string]bool, len(codes))
		for _, c := range codes {
			expiredSet[c] = true
		}

		sl, err := readSteamLink(ctx, fs, uid)
		if err != nil {
			continue
		}
		var cleaned []string
		cleanedRetries := make(map[string]int)
		for _, c := range sl.FailedSharecodes {
			if !expiredSet[c] {
				cleaned = append(cleaned, c)
				if r, ok := sl.FailedRetries[c]; ok {
					cleanedRetries[c] = r
				}
			}
		}
		if len(cleaned) < len(sl.FailedSharecodes) {
			updates := map[string]interface{}{
				"failedSharecodes": cleaned,
				"failedRetries":    cleanedRetries,
			}
			if err := updateSteamLink(ctx, fs, uid, updates); err != nil {
				log.Printf("[steam-cron] failed to clean failedSharecodes for %s: %v", uid, err)
			} else {
				log.Printf("[steam-cron] removed %d expired sharecodes from user %s", len(sl.FailedSharecodes)-len(cleaned), uid)
			}
		}
	}
}
