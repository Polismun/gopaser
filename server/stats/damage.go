package stats

import "math"

// DamageStatsResult holds damage-based stats for a player.
type DamageStatsResult struct {
	TotalDamage      int
	ADR              float64
	DamageByHitgroup map[string]int
}

// ComputeDamageStats computes ADR (clamped 100/victim/round) and damage breakdown.
func ComputeDamageStats(damages []DamageEvent, boundaries []RoundBoundary, playerName string) DamageStatsResult {
	totalRounds := len(boundaries)
	totalDamage := 0
	damageByHitgroup := make(map[string]int)

	for i := range damages {
		d := &damages[i]
		if d.AttackerName != playerName {
			continue
		}
		totalDamage += d.Damage
		hg := d.HitGroup
		if hg == "" {
			hg = "body"
		}
		damageByHitgroup[hg] += d.Damage
	}

	// Clamped ADR: cap per-round damage at 100 per victim
	damagesByRound := GroupDamagesByRound(damages, boundaries)
	clampedTotal := 0
	for _, roundDmgs := range damagesByRound {
		perVictim := make(map[string]int)
		for i := range roundDmgs {
			d := &roundDmgs[i]
			if d.AttackerName != playerName {
				continue
			}
			cur := perVictim[d.VictimName]
			added := cur + d.Damage
			if added > 100 {
				added = 100
			}
			perVictim[d.VictimName] = added
		}
		for _, dmg := range perVictim {
			clampedTotal += dmg
		}
	}

	var adr float64
	if totalRounds > 0 {
		adr = math.Round(float64(clampedTotal)/float64(totalRounds)*10) / 10
	}

	return DamageStatsResult{
		TotalDamage:      totalDamage,
		ADR:              adr,
		DamageByHitgroup: damageByHitgroup,
	}
}
