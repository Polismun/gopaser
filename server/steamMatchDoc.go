package main

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/firestore"
)

// MatchPlayer mirrors the TS MatchPlayer type.
type MatchPlayer struct {
	AccountID  uint64  `firestore:"accountId" json:"accountId"`
	Team       string  `firestore:"team" json:"team"`
	Name       string  `firestore:"name,omitempty" json:"name,omitempty"`
	Kills      int     `firestore:"kills" json:"kills"`
	Deaths     int     `firestore:"deaths" json:"deaths"`
	Assists    int     `firestore:"assists" json:"assists"`
	Score      int     `firestore:"score" json:"score"`
	MVPs       int     `firestore:"mvps" json:"mvps"`
	Headshots  int     `firestore:"headshots" json:"headshots"`
	RankID     int     `firestore:"rankId,omitempty" json:"rankId,omitempty"`
	RankChange int     `firestore:"rankChange,omitempty" json:"rankChange,omitempty"`
	RankType   int     `firestore:"rankType,omitempty" json:"rankType,omitempty"`
	Wins       int     `firestore:"wins,omitempty" json:"wins,omitempty"`
	HLTVRating float64 `firestore:"hltvRating,omitempty" json:"hltvRating,omitempty"`
}

// MatchDoc mirrors the TS MatchDoc type.
type MatchDoc struct {
	ID          string        `firestore:"id" json:"id"`
	OwnerID     string        `firestore:"ownerId" json:"ownerId"`
	Status      string        `firestore:"status" json:"status"`
	Source      string        `firestore:"source" json:"source"`
	MatchDate   string        `firestore:"matchDate" json:"matchDate"`
	Sharecode   string        `firestore:"sharecode,omitempty" json:"sharecode,omitempty"`
	GCMatchID   string        `firestore:"gcMatchId,omitempty" json:"gcMatchId,omitempty"`
	Map         string        `firestore:"map" json:"map"`
	TeamScores  [2]int        `firestore:"teamScores" json:"teamScores"`
	Duration    int           `firestore:"duration,omitempty" json:"duration,omitempty"`
	Players     []MatchPlayer `firestore:"players" json:"players"`
	DemoFileID  string        `firestore:"demoFileId,omitempty" json:"demoFileId,omitempty"`
	DemoStatsID string        `firestore:"demoStatsId,omitempty" json:"demoStatsId,omitempty"`
	TeamCT      string        `firestore:"teamCT,omitempty" json:"teamCT,omitempty"`
	TeamT       string        `firestore:"teamT,omitempty" json:"teamT,omitempty"`
	CreatedAt   string        `firestore:"createdAt" json:"createdAt"`
}

// createMatchDoc creates a new MatchDoc in Firestore.
func createMatchDoc(ctx context.Context, fs *firestore.Client, match MatchDoc) error {
	_, err := fs.Collection("matches").Doc(match.ID).Set(ctx, match)
	return err
}

// enrichMatchDoc enriches a pending MatchDoc after demo parse → status:'parsed'.
func enrichMatchDoc(ctx context.Context, fs *firestore.Client, matchID string, enrichment matchEnrichment) error {
	ref := fs.Collection("matches").Doc(matchID)
	snap, err := ref.Get(ctx)
	if err != nil {
		return fmt.Errorf("match %s not found: %w", matchID, err)
	}

	var match MatchDoc
	if err := snap.DataTo(&match); err != nil {
		return err
	}

	// Merge enrichments into existing players by accountId
	for i, p := range match.Players {
		for _, e := range enrichment.PlayerEnrichments {
			if e.AccountID == p.AccountID {
				match.Players[i].Name = e.Name
				if e.RankID != 0 {
					match.Players[i].RankID = e.RankID
				}
				if e.RankType != 0 {
					match.Players[i].RankType = e.RankType
				}
				if e.Wins != 0 {
					match.Players[i].Wins = e.Wins
				}
				if e.HLTVRating != 0 {
					match.Players[i].HLTVRating = e.HLTVRating
				}
				break
			}
		}
	}

	updates := []firestore.Update{
		{Path: "status", Value: "parsed"},
		{Path: "demoFileId", Value: enrichment.DemoFileID},
		{Path: "demoStatsId", Value: enrichment.DemoStatsID},
		{Path: "teamCT", Value: enrichment.TeamCT},
		{Path: "teamT", Value: enrichment.TeamT},
		{Path: "players", Value: match.Players},
	}
	if enrichment.MapName != "" {
		updates = append(updates, firestore.Update{Path: "map", Value: enrichment.MapName})
	}

	_, err = ref.Update(ctx, updates)
	return err
}

type matchEnrichment struct {
	DemoFileID        string
	DemoStatsID       string
	TeamCT            string
	TeamT             string
	MapName           string
	PlayerEnrichments []playerEnrichment
}

type playerEnrichment struct {
	AccountID  uint64
	Name       string
	RankID     int
	RankType   int
	Wins       int
	HLTVRating float64
}

// matchExistsBySharecode checks if a MatchDoc with this sharecode exists for the user.
func matchExistsBySharecode(ctx context.Context, fs *firestore.Client, ownerID, sharecode string) (bool, error) {
	iter := fs.Collection("matches").
		Where("ownerId", "==", ownerID).
		Where("sharecode", "==", sharecode).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()

	_, err := iter.Next()
	if err != nil {
		return false, nil // no match found or error → treat as not existing
	}
	return true, nil
}

// findDemoBySharcode finds a DemoDoc by steamSharecode (any owner).
type demoSharecodeHit struct {
	OwnerID    string
	VpsFileID  string
	DemoHash   string
	RecordedAt string
}

func findDemoBySharcode(ctx context.Context, fs *firestore.Client, sharecode string) (*demoSharecodeHit, error) {
	iter := fs.Collection("demos").
		Where("steamSharecode", "==", sharecode).
		Limit(1).
		Documents(ctx)
	defer iter.Stop()

	doc, err := iter.Next()
	if err != nil {
		return nil, nil // not found
	}

	data := doc.Data()
	return &demoSharecodeHit{
		OwnerID:    strVal(data, "ownerId"),
		VpsFileID:  strVal(data, "vpsFileId"),
		DemoHash:   strVal(data, "demoHash"),
		RecordedAt: strVal(data, "recordedAt"),
	}, nil
}

// saveDemoStats writes a DemoStatsResult to Firestore demoStats/{id}.
func saveDemoStats(ctx context.Context, fs *firestore.Client, demoID string, stats interface{}) error {
	_, err := fs.Collection("demoStats").Doc(demoID).Set(ctx, stats)
	return err
}

// saveDemoHash registers a hash in the demoHashes collection.
func saveDemoHash(ctx context.Context, fs *firestore.Client, hash string, data map[string]interface{}) error {
	_, err := fs.Collection("demoHashes").Doc(hash).Set(ctx, data)
	return err
}

// createDemoDoc creates a DemoDoc in Firestore.
func createDemoDoc(ctx context.Context, fs *firestore.Client, demoID string, data map[string]interface{}) error {
	_, err := fs.Collection("demos").Doc(demoID).Set(ctx, data)
	return err
}

// readSteamLink reads the steamLink from a user doc.
type steamLinkData struct {
	SteamID            string   `firestore:"steamId"`
	AuthCodeEncrypted  string   `firestore:"authCodeEncrypted"`
	AuthCodeIV         string   `firestore:"authCodeIv"`
	LatestSharecode    string   `firestore:"latestSharecode"`
	FailedSharecodes   []string `firestore:"failedSharecodes"`
	LastSyncAt         string   `firestore:"lastSyncAt"`
}

func readSteamLink(ctx context.Context, fs *firestore.Client, uid string) (*steamLinkData, error) {
	snap, err := fs.Collection("users").Doc(uid).Get(ctx)
	if err != nil {
		return nil, fmt.Errorf("user %s not found: %w", uid, err)
	}

	data := snap.Data()
	sl, ok := data["steamLink"].(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("no steamLink for user %s", uid)
	}

	return &steamLinkData{
		SteamID:           strVal(sl, "steamId"),
		AuthCodeEncrypted: strVal(sl, "authCodeEncrypted"),
		AuthCodeIV:        strVal(sl, "authCodeIv"),
		LatestSharecode:   strVal(sl, "latestSharecode"),
		FailedSharecodes:  strSlice(sl, "failedSharecodes"),
		LastSyncAt:        strVal(sl, "lastSyncAt"),
	}, nil
}

// updateSteamLink updates the steamLink cursor after sync.
func updateSteamLink(ctx context.Context, fs *firestore.Client, uid string, updates map[string]interface{}) error {
	// Build dot-notation updates for nested steamLink fields
	flat := make([]firestore.Update, 0, len(updates))
	for k, v := range updates {
		flat = append(flat, firestore.Update{Path: "steamLink." + k, Value: v})
	}
	_, err := fs.Collection("users").Doc(uid).Update(ctx, flat)
	return err
}

// nowISO returns the current time as ISO-8601 string.
func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// strVal safely extracts a string from a map.
func strVal(m map[string]interface{}, key string) string {
	if v, ok := m[key].(string); ok {
		return v
	}
	return ""
}

// strSlice safely extracts a string slice from a map.
func strSlice(m map[string]interface{}, key string) []string {
	arr, ok := m[key].([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(arr))
	for _, v := range arr {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}
