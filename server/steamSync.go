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

	"cloud.google.com/go/firestore"
	"cs2-parser-server/stats"
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
				if ex, st, mid := matchExistsBySharecode(ctx, fs, uid, code); ex && st == "pending" {
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
		"lastSyncAt":           nowISO(),
		"lastSyncImportedCount": imported,
		"failedSharecodes":     newFailed,
		"failedRetries":        newRetries,
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
	exists, matchStatus, existingMatchID := matchExistsBySharecode(ctx, fs, uid, code)
	if exists && matchStatus == "parsed" {
		return nil // fully processed
	}
	if exists && matchStatus == "failed" {
		return nil // permanently failed, don't retry
	}
	// If exists but stuck pending (no demo), we'll re-process from download step
	resumePending := exists && matchStatus == "pending"

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

	// 2. Create MatchDoc pending (skip if resuming a stuck pending)
	var matchDocID string
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

		pendingMatch := MatchDoc{
			ID:         matchDocID,
			OwnerID:    uid,
			Status:     "pending",
			Source:     "steam",
			MatchDate:  matchDate,
			Sharecode:  code,
			GCMatchID:  gcResult.MatchID,
			Map:        gcResult.Map,
			TeamScores: func() [2]int {
				if len(gcResult.TeamScores) >= 2 {
					return [2]int{gcResult.TeamScores[0], gcResult.TeamScores[1]}
				}
				return [2]int{}
			}(),
			Duration:   gcResult.Duration,
			Players:    gcPlayers,
			CreatedAt:  nowISO(),
		}
		if err := createMatchDoc(ctx, fs, pendingMatch); err != nil {
			log.Printf("[steam-sync] failed to create pending MatchDoc: %v", err)
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

	// Hash dedup
	hashExists, existingFileID, _ := checkDemoHashViaVercel(dl.sha256Hex, idToken)
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
		"id":              parsed.ID,
		"vpsFileId":       parsed.ID,
		"ownerId":         uid,
		"demoHash":        dl.sha256Hex,
		"source":          "steam-auto",
		"steamSharecode":  code,
		"visibility":      "private",
		"createdAt":       createdAt,
		"mapName":         parseResult.MapName,
		"teamCT":          teamCT,
		"teamT":           teamT,
		"scoreCT":         scoreCT,
		"scoreT":          scoreT,
		"tickRate":        parseResult.TickRate,
		"totalRounds":     allStats.TotalRounds,
		"fileSizeBytes":   parsed.SizeBytes,
	}
	if dl.recordedAt != "" {
		demoData["recordedAt"] = dl.recordedAt
	}
	// Add players array
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

	if err := createDemoDoc(ctx, fs, parsed.ID, demoData); err != nil {
		log.Printf("[steam-sync] DemoDoc write failed: %v", err)
		return fmt.Errorf("VPS_ERROR:DemoDoc write: %v", err)
	}

	// demoStats
	if err := saveDemoStats(ctx, fs, parsed.ID, allStats); err != nil {
		log.Printf("[steam-sync] demoStats write failed: %v", err)
		// Non-fatal: match still works without detailed stats
	}

	// demoHash
	hashData := map[string]interface{}{
		"vpsFileId":     parsed.ID,
		"mapName":       parseResult.MapName,
		"teamCT":        teamCT,
		"teamT":         teamT,
		"scoreCT":       scoreCT,
		"scoreT":        scoreT,
		"tickRate":      parseResult.TickRate,
		"totalRounds":   allStats.TotalRounds,
		"fileSizeBytes": parsed.SizeBytes,
		"players":       demoPlayers,
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
		newMatch := MatchDoc{
			ID:         newMatchID,
			OwnerID:    uid,
			Status:     "parsed",
			Source:     "steam",
			MatchDate:  ra,
			Sharecode:  code,
			Map:        strVal(meta, "mapName"),
			TeamScores: [2]int{scoreCT, scoreT},
			Players:    matchPlayers,
			DemoFileID: vpsFileID,
			DemoStatsID: vpsFileID,
			TeamCT:     strVal(meta, "teamCT"),
			TeamT:      strVal(meta, "teamT"),
			CreatedAt:  nowISO(),
		}
		if err := createMatchDoc(ctx, fs, newMatch); err != nil {
			log.Printf("[steam-sync] dedup MatchDoc create failed: %v", err)
		}
	}

	return nil
}

// ── GC bot internal call ──

type gcBotResult struct {
	URL        string         `json:"url"`
	Matchtime  int            `json:"matchtime"`
	MatchID    string         `json:"matchId"`
	Map        string         `json:"map"`
	Duration   int            `json:"duration"`
	TeamScores [2]int         `json:"teamScores"`
	Players    []gcBotPlayer  `json:"players"`
}

type gcBotPlayer struct {
	AccountID  int64  `json:"accountId"`
	Kills      int    `json:"kills"`
	Deaths     int    `json:"deaths"`
	Assists    int    `json:"assists"`
	Score      int    `json:"score"`
	MVPs       int    `json:"mvps"`
	Headshots  int    `json:"headshots"`
	RankID     int    `json:"rankId"`
	RankChange int    `json:"rankChange"`
	RankType   int    `json:"rankType"`
	Wins       int    `json:"wins"`
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
		sid := strVal(p, "steamId")
		accountID := int64(0)
		if sid != "" {
			accountID = steamID64ToAccountID(sid)
		}
		mp := MatchPlayer{
			AccountID: accountID,
			Name:      strVal(p, "name"),
			Team:      strVal(p, "team"),
			Kills:     intVal(p, "kills"),
			Deaths:    intVal(p, "deaths"),
			Assists:   intVal(p, "assists"),
			RankID:    intVal(p, "rank"),
			RankType:  intVal(p, "rankType"),
			Wins:      intVal(p, "wins"),
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
