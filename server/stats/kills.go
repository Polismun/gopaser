package stats

import "math"

// KillStatsResult holds kill-based stats for a player.
type KillStatsResult struct {
	HSKills         int
	HSPercent       float64
	WeaponKills     map[string]WeaponKillStats
	OpeningKills    int
	OpeningDeaths   int
	TradeKills      int
	TradedDeaths    int
	MultiKillRounds map[int]int // key = kill count (2-5), value = number of rounds
	FlashAssists    int
}

// ComputeKillStats computes HS%, weapon breakdown, opening kills/deaths, trades, multi-kills, flash assists.
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

	// Opening kills/deaths + multi-kills: per-round analysis
	openingKills := 0
	openingDeaths := 0
	multiKillRounds := make(map[int]int)

	killsByRound := GroupKillsByRound(kills, boundaries)
	for _, roundKills := range killsByRound {
		if len(roundKills) == 0 {
			continue
		}
		SortKillsByTick(roundKills)

		// Opening kill/death
		first := roundKills[0]
		if first.KillerName == playerName {
			openingKills++
		}
		if first.VictimName == playerName {
			openingDeaths++
		}

		// Multi-kills: count player kills in this round
		roundPlayerKills := 0
		for _, k := range roundKills {
			if k.KillerName == playerName {
				roundPlayerKills++
			}
		}
		if roundPlayerKills >= 2 {
			multiKillRounds[roundPlayerKills]++
		}
	}

	// Trade kills & traded deaths
	tradeKills := 0
	tradedDeaths := 0
	sortedAll := make([]KillEvent, len(kills))
	copy(sortedAll, kills)
	SortKillsByTick(sortedAll)

	for i, k := range sortedAll {
		// Trade kill: teammate died, player avenges within window
		if k.KillerName == playerName {
			// Look back for a teammate death by this victim
			for j := i - 1; j >= 0; j-- {
				prev := sortedAll[j]
				if k.Tick-prev.Tick > tradeWindowTicks {
					break
				}
				// Teammate was killed by the person we just killed
				if prev.VictimTeam == k.KillerTeam && prev.VictimName != playerName && prev.KillerName == k.VictimName {
					tradeKills++
					break
				}
			}
		}
		// Traded death: player dies, teammate avenges within window
		if k.VictimName == playerName {
			for j := i + 1; j < len(sortedAll); j++ {
				next := sortedAll[j]
				if next.Tick-k.Tick > tradeWindowTicks {
					break
				}
				// Teammate killed the person who killed us
				if next.KillerTeam == k.VictimTeam && next.KillerName != playerName && next.VictimName == k.KillerName {
					tradedDeaths++
					break
				}
			}
		}
	}

	// Flash assists: kills where this player flashed the victim
	flashAssists := 0
	for _, k := range kills {
		if k.IsAssistedFlash && k.AssisterName == playerName {
			flashAssists++
		}
	}

	return KillStatsResult{
		HSKills:         hsKills,
		HSPercent:       hsPercent,
		WeaponKills:     weaponKills,
		OpeningKills:    openingKills,
		OpeningDeaths:   openingDeaths,
		TradeKills:      tradeKills,
		TradedDeaths:    tradedDeaths,
		MultiKillRounds: multiKillRounds,
		FlashAssists:    flashAssists,
	}
}
