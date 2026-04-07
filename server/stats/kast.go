package stats

import "math"

const tradeWindowTicks = 320 // ~5 seconds at 64 tick

// ComputeKAST computes KAST% for a single player.
// KAST = % of rounds with Kill, Assist, Survived, or Traded.
func ComputeKAST(kills []KillEvent, ticks []TickData, boundaries []RoundBoundary, playerName string) float64 {
	if len(boundaries) == 0 {
		return 0
	}

	killsByRound := GroupKillsByRound(kills, boundaries)
	contributing := 0

	for _, b := range boundaries {
		roundKills := killsByRound[b.RoundNumber]

		gotKill := false
		gotAssist := false
		died := false
		wasTraded := false
		var deathTick int
		var killerName string

		for _, k := range roundKills {
			if k.KillerName == playerName {
				gotKill = true
			}
			if k.AssisterName == playerName {
				gotAssist = true
			}
			if k.VictimName == playerName {
				died = true
				deathTick = k.Tick
				killerName = k.KillerName
			}
		}

		survived := !died

		// Check if traded: player died but a teammate killed the player's killer within 5 seconds
		if died && killerName != "" {
			for _, k := range roundKills {
				if k.KillerName != playerName &&
					k.VictimName == killerName &&
					k.Tick > deathTick &&
					k.Tick <= deathTick+tradeWindowTicks {
					wasTraded = true
					break
				}
			}
		}

		if gotKill || gotAssist || survived || wasTraded {
			contributing++
		}
	}

	return math.Round(float64(contributing)/float64(len(boundaries))*1000) / 10
}
