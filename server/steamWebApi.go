package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	steamAPIBase    = "https://api.steampowered.com"
	steamAPITimeout = 10 * time.Second
)

var steamBackoffMs = []int{1000, 2000, 4000}

// getNextMatchSharingCode fetches the next sharecode after knownCode from Steam Web API.
// Returns "" when there are no more new matches (Steam returns "n/a").
func getNextMatchSharingCode(steamID, authCode, knownCode string) (string, error) {
	apiKey := os.Getenv("STEAM_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("STEAM_API_KEY not set")
	}

	params := url.Values{
		"key":         {apiKey},
		"steamid":     {steamID},
		"steamidkey":  {authCode},
		"knowncode":   {knownCode},
	}
	apiURL := steamAPIBase + "/ICSGOPlayers_730/GetNextMatchSharingCode/v1/?" + params.Encode()

	client := &http.Client{Timeout: steamAPITimeout}

	for attempt := 0; attempt <= len(steamBackoffMs); attempt++ {
		resp, err := client.Get(apiURL)
		if err != nil {
			return "", fmt.Errorf("steam API request failed: %w", err)
		}

		if resp.StatusCode == 403 {
			resp.Body.Close()
			return "", fmt.Errorf("INVALID_AUTH_CODE")
		}
		if resp.StatusCode == 202 {
			resp.Body.Close()
			return "", nil // Steam still processing, treat as no-next
		}
		if resp.StatusCode == 429 {
			resp.Body.Close()
			if attempt < len(steamBackoffMs) {
				time.Sleep(time.Duration(steamBackoffMs[attempt]) * time.Millisecond)
				continue
			}
			return "", fmt.Errorf("STEAM_API_429")
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return "", fmt.Errorf("steam API HTTP %d", resp.StatusCode)
		}

		var body struct {
			Result *struct {
				Nextcode string `json:"nextcode"`
			} `json:"result"`
		}
		err = json.NewDecoder(resp.Body).Decode(&body)
		resp.Body.Close()
		if err != nil {
			return "", nil
		}

		next := ""
		if body.Result != nil {
			next = body.Result.Nextcode
		}
		if next == "" || next == "n/a" {
			return "", nil
		}
		return next, nil
	}

	return "", fmt.Errorf("STEAM_API_429")
}

// fetchPlayerNames fetches persona names for up to 100 SteamID64s in a single API call.
// Returns a map[steamId64]personaName. Best-effort — returns empty map on failure.
func fetchPlayerNames(steamIDs []string) map[string]string {
	result := make(map[string]string)
	apiKey := os.Getenv("STEAM_API_KEY")
	if apiKey == "" || len(steamIDs) == 0 {
		return result
	}

	params := url.Values{
		"key":      {apiKey},
		"steamids": {strings.Join(steamIDs, ",")},
	}
	apiURL := steamAPIBase + "/ISteamUser/GetPlayerSummaries/v2/?" + params.Encode()

	client := &http.Client{Timeout: steamAPITimeout}
	resp, err := client.Get(apiURL)
	if err != nil || resp.StatusCode != 200 {
		if resp != nil {
			resp.Body.Close()
		}
		return result
	}
	defer resp.Body.Close()

	var body struct {
		Response struct {
			Players []struct {
				SteamID     string `json:"steamid"`
				PersonaName string `json:"personaname"`
			} `json:"players"`
		} `json:"response"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return result
	}

	for _, p := range body.Response.Players {
		if p.PersonaName != "" {
			result[p.SteamID] = p.PersonaName
		}
	}
	return result
}

// accountIDToSteamID64 converts a SteamID32 (account_id) to SteamID64.
const steamIDBase int64 = 76561197960265728

func accountIDToSteamID64(accountID int64) string {
	return fmt.Sprintf("%d", accountID+steamIDBase)
}

// steamID64ToAccountID converts a SteamID64 to SteamID32 (account_id).
func steamID64ToAccountID(steamID64 string) int64 {
	var id int64
	fmt.Sscanf(steamID64, "%d", &id)
	if id > steamIDBase {
		return id - steamIDBase
	}
	return 0
}
