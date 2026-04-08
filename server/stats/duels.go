package stats

// ComputeDuels computes head-to-head kill records for a player.
func ComputeDuels(kills []KillEvent, playerName string) map[string]DuelRecord {
	duels := make(map[string]DuelRecord)
	for _, k := range kills {
		if k.KillerName == playerName && k.VictimName != "" {
			rec := duels[k.VictimName]
			rec.Kills++
			duels[k.VictimName] = rec
		}
		if k.VictimName == playerName && k.KillerName != "" {
			rec := duels[k.KillerName]
			rec.Deaths++
			duels[k.KillerName] = rec
		}
	}
	return duels
}
