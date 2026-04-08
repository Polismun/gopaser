package stats

// ClutchRecord holds wins/attempts for a specific 1vN situation.
type ClutchRecord struct {
	Wins     int `json:"wins" firestore:"wins"`
	Attempts int `json:"attempts" firestore:"attempts"`
}

// ClutchStatsResult holds clutch stats for a player.
type ClutchStatsResult struct {
	ClutchWins      int
	ClutchAttempts  int
	ClutchBreakdown map[int]ClutchRecord
	ClutchEvents    []ClutchEvent
}

// ComputeClutchStats detects 1vN clutch attempts and wins, with per-event details.
func ComputeClutchStats(kills []KillEvent, ticks []TickData, boundaries []RoundBoundary, playerName, playerTeam string) ClutchStatsResult {
	clutchWins := 0
	clutchAttempts := 0
	breakdown := make(map[int]ClutchRecord)
	var events []ClutchEvent

	killsByRound := GroupKillsByRound(kills, boundaries)

	for _, b := range boundaries {
		roundKills := killsByRound[b.RoundNumber]
		if len(roundKills) == 0 {
			continue
		}

		sorted := make([]KillEvent, len(roundKills))
		copy(sorted, roundKills)
		SortKillsByTick(sorted)

		startTick := FindTickAt(ticks, b.StartTick)
		if startTick == nil {
			continue
		}

		aliveSet := make(map[string]bool)
		teamByPlayer := make(map[string]string)
		for _, p := range startTick.Players {
			if p.IsAlive {
				aliveSet[p.Name] = true
			}
			teamByPlayer[p.Name] = p.Team
		}

		isClutching := false
		clutchN := 0
		clutchKills := 0

		for _, kill := range sorted {
			delete(aliveSet, kill.VictimName)

			if !aliveSet[playerName] {
				continue
			}

			aliveTeammates := countAliveOnTeam(aliveSet, teamByPlayer, playerTeam)
			enemyTeam := "T"
			if playerTeam == "T" {
				enemyTeam = "CT"
			}
			aliveEnemies := countAliveOnTeam(aliveSet, teamByPlayer, enemyTeam)

			if aliveTeammates == 1 && aliveEnemies > 0 && !isClutching {
				isClutching = true
				clutchN = aliveEnemies
				clutchKills = 0
				clutchAttempts++
				rec := breakdown[clutchN]
				rec.Attempts++
				breakdown[clutchN] = rec
			}

			// Count kills during clutch
			if isClutching && kill.KillerName == playerName {
				clutchKills++
			}
		}

		if isClutching {
			won := b.Winner == playerTeam

			if won {
				clutchWins++
				rec := breakdown[clutchN]
				rec.Wins++
				breakdown[clutchN] = rec
			}

			// Save detection: player alive at round end but round lost
			saved := false
			if !won && aliveSet[playerName] {
				saved = true
			}

			events = append(events, ClutchEvent{
				Round:   b.RoundNumber,
				Enemies: clutchN,
				Won:     won,
				Kills:   clutchKills,
				Saved:   saved,
			})
		}
	}

	return ClutchStatsResult{
		ClutchWins:      clutchWins,
		ClutchAttempts:  clutchAttempts,
		ClutchBreakdown: breakdown,
		ClutchEvents:    events,
	}
}

func countAliveOnTeam(aliveSet map[string]bool, teamByPlayer map[string]string, team string) int {
	count := 0
	for name := range aliveSet {
		if teamByPlayer[name] == team {
			count++
		}
	}
	return count
}
