package stats

// ClutchStatsResult holds clutch stats for a player.
type ClutchStatsResult struct {
	ClutchWins     int
	ClutchAttempts int
}

// ComputeClutchStats detects 1vN clutch attempts and wins.
func ComputeClutchStats(kills []KillEvent, ticks []TickData, boundaries []RoundBoundary, playerName, playerTeam string) ClutchStatsResult {
	clutchWins := 0
	clutchAttempts := 0

	killsByRound := GroupKillsByRound(kills, boundaries)

	for _, b := range boundaries {
		roundKills := killsByRound[b.RoundNumber]
		if len(roundKills) == 0 {
			continue
		}

		sorted := make([]KillEvent, len(roundKills))
		copy(sorted, roundKills)
		SortKillsByTick(sorted)

		// Build initial alive set from first tick of round
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

		for _, kill := range sorted {
			delete(aliveSet, kill.VictimName)

			if !aliveSet[playerName] {
				continue // player dead
			}

			aliveTeammates := countAliveOnTeam(aliveSet, teamByPlayer, playerTeam)
			enemyTeam := "T"
			if playerTeam == "T" {
				enemyTeam = "CT"
			}
			aliveEnemies := countAliveOnTeam(aliveSet, teamByPlayer, enemyTeam)

			if aliveTeammates == 1 && aliveEnemies > 0 && !isClutching {
				isClutching = true
				clutchAttempts++
			}
		}

		if isClutching && b.Winner == playerTeam {
			clutchWins++
		}
	}

	return ClutchStatsResult{ClutchWins: clutchWins, ClutchAttempts: clutchAttempts}
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
