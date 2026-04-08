package stats

// BombStatsResult holds bomb plant/defuse counts for a player.
type BombStatsResult struct {
	Plants  int
	Defuses int
}

// ComputeBombStats counts bomb plants and defuses for a player.
// Plant: the player is listed as plantingPlayerName in a tick where bombPlantTick > 0.
// Defuse: the player is listed as defusingPlayerName in a round that ends with CT win
// after the bomb was planted (bombPlantTick > 0 earlier in the round).
func ComputeBombStats(ticks []TickData, boundaries []RoundBoundary, playerName string) BombStatsResult {
	plants := 0
	defuses := 0

	for _, b := range boundaries {
		plantedByPlayer := false
		bombWasPlanted := false
		defusingPlayer := false

		for _, t := range ticks {
			if t.Tick < b.StartTick || t.Tick > b.EndTick {
				continue
			}

			hasBomb := t.BombPlantTick != nil && *t.BombPlantTick > 0

			// Detect plant: player was planting and bomb is now planted
			if hasBomb && t.PlantingPlayerName == playerName && !plantedByPlayer {
				plantedByPlayer = true
			}
			// Also catch: plantingPlayerName persists on the tick where bomb gets planted
			if hasBomb && !plantedByPlayer {
				// Check if in a previous tick of this round, this player was planting
				// We track via the name appearing as planter
			}

			if hasBomb {
				bombWasPlanted = true
			}

			// Detect defuse: player is defusing
			if t.IsDefusing && t.DefusingPlayerName == playerName {
				defusingPlayer = true
			}
		}

		if plantedByPlayer {
			plants++
		}
		// Defuse: player was defusing + CT won + bomb was planted
		if defusingPlayer && bombWasPlanted && b.Winner == "CT" {
			defuses++
		}
	}

	return BombStatsResult{Plants: plants, Defuses: defuses}
}
