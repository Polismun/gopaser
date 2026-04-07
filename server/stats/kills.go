package stats

import "math"

// KillStatsResult holds kill-based stats for a player.
type KillStatsResult struct {
	HSKills       int
	HSPercent     float64
	WeaponKills   map[string]WeaponKillStats
	OpeningKills  int
	OpeningDeaths int
}

// ComputeKillStats computes HS%, weapon breakdown, and opening kills/deaths.
func ComputeKillStats(kills []KillEvent, boundaries []RoundBoundary, playerName string) KillStatsResult {
	var playerKills []KillEvent
	for i := range kills {
		if kills[i].KillerName == playerName {
			playerKills = append(playerKills, kills[i])
		}
	}
	totalKills := len(playerKills)

	// Headshot stats
	hsKills := 0
	for _, k := range playerKills {
		if k.IsHeadshot {
			hsKills++
		}
	}
	var hsPercent float64
	if totalKills > 0 {
		hsPercent = math.Round(float64(hsKills)/float64(totalKills)*1000) / 10
	}

	// Weapon kills breakdown
	weaponKills := make(map[string]WeaponKillStats)
	for _, k := range playerKills {
		weapon := k.Weapon
		if weapon == "" {
			weapon = "Unknown"
		}
		wk := weaponKills[weapon]
		wk.Kills++
		if k.IsHeadshot {
			wk.HSKills++
		}
		weaponKills[weapon] = wk
	}

	// Opening kills/deaths: first kill of each round
	openingKills := 0
	openingDeaths := 0
	killsByRound := GroupKillsByRound(kills, boundaries)
	for _, roundKills := range killsByRound {
		if len(roundKills) == 0 {
			continue
		}
		SortKillsByTick(roundKills)
		first := roundKills[0]
		if first.KillerName == playerName {
			openingKills++
		}
		if first.VictimName == playerName {
			openingDeaths++
		}
	}

	return KillStatsResult{
		HSKills:       hsKills,
		HSPercent:     hsPercent,
		WeaponKills:   weaponKills,
		OpeningKills:  openingKills,
		OpeningDeaths: openingDeaths,
	}
}
