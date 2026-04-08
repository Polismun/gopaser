package stats

// ComputeRoundEconomy extracts money + equipment at the start of each round for a player.
func ComputeRoundEconomy(ticks []TickData, boundaries []RoundBoundary, playerName string) []RoundEcon {
	var result []RoundEcon

	for _, b := range boundaries {
		startTick := FindTickAt(ticks, b.StartTick)
		if startTick == nil {
			continue
		}

		for _, p := range startTick.Players {
			if p.Name == playerName {
				equip := make([]string, len(p.Equipment))
				copy(equip, p.Equipment)
				result = append(result, RoundEcon{
					Round:     b.RoundNumber,
					Money:     p.Money,
					Equipment: equip,
				})
				break
			}
		}
	}

	return result
}
