package stats

// ComputeUtilityStats counts grenade throws per type for a player.
func ComputeUtilityStats(grenadeEvents []GrenadeEvent, playerName string) UtilityCount {
	var result UtilityCount

	for i := range grenadeEvents {
		e := &grenadeEvents[i]
		if e.Action != "throw" || e.ThrowerName != playerName {
			continue
		}
		switch e.Type {
		case "smoke":
			result.Smoke++
		case "flash":
			result.Flash++
		case "he":
			result.HE++
		case "molotov", "incendiary":
			result.Molotov++
		// decoy intentionally ignored
		}
	}

	return result
}
