package stats

import "sort"

// BuildRoundBoundaries extracts round start/end ticks from tick data.
func BuildRoundBoundaries(ticks []TickData) []RoundBoundary {
	var boundaries []RoundBoundary
	var current *struct {
		tick        int
		roundNumber int
	}

	for i := range ticks {
		t := &ticks[i]
		if t.IsRoundStart && t.RoundNumber != nil {
			current = &struct {
				tick        int
				roundNumber int
			}{t.Tick, *t.RoundNumber}
		}
		if t.IsRoundEnd && current != nil {
			boundaries = append(boundaries, RoundBoundary{
				RoundNumber: current.roundNumber,
				StartTick:   current.tick,
				EndTick:     t.Tick,
				Winner:      t.Winner,
			})
			current = nil
		}
	}

	// Handle last round without isRoundEnd
	if current != nil && len(ticks) > 0 {
		boundaries = append(boundaries, RoundBoundary{
			RoundNumber: current.roundNumber,
			StartTick:   current.tick,
			EndTick:     ticks[len(ticks)-1].Tick,
			Winner:      "",
		})
	}

	return boundaries
}

// findRound returns the round number for a given tick using binary search on boundaries.
// Boundaries must be sorted by StartTick (which BuildRoundBoundaries guarantees).
func findRound(boundaries []RoundBoundary, tick int) int {
	lo, hi := 0, len(boundaries)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		b := &boundaries[mid]
		if tick < b.StartTick {
			hi = mid - 1
		} else if tick > b.EndTick {
			lo = mid + 1
		} else {
			return b.RoundNumber
		}
	}
	return -1
}

// GroupKillsByRound assigns kill events to rounds by tick range (binary search).
func GroupKillsByRound(kills []KillEvent, boundaries []RoundBoundary) map[int][]KillEvent {
	m := make(map[int][]KillEvent, len(boundaries))
	for _, b := range boundaries {
		m[b.RoundNumber] = nil
	}
	for i := range kills {
		r := findRound(boundaries, kills[i].Tick)
		if r >= 0 {
			m[r] = append(m[r], kills[i])
		}
	}
	return m
}

// GroupDamagesByRound assigns damage events to rounds by tick range (binary search).
func GroupDamagesByRound(damages []DamageEvent, boundaries []RoundBoundary) map[int][]DamageEvent {
	m := make(map[int][]DamageEvent, len(boundaries))
	for _, b := range boundaries {
		m[b.RoundNumber] = nil
	}
	for i := range damages {
		r := findRound(boundaries, damages[i].Tick)
		if r >= 0 {
			m[r] = append(m[r], damages[i])
		}
	}
	return m
}

// SortKillsByTick sorts a slice of kills chronologically.
func SortKillsByTick(kills []KillEvent) {
	sort.Slice(kills, func(i, j int) bool { return kills[i].Tick < kills[j].Tick })
}

// ComputeRoundsSurvived counts rounds where the player is alive at the end.
func ComputeRoundsSurvived(ticks []TickData, boundaries []RoundBoundary, playerName string) int {
	survived := 0
	for _, b := range boundaries {
		endTick := FindTickAt(ticks, b.EndTick)
		if endTick == nil {
			continue
		}
		for _, p := range endTick.Players {
			if p.Name == playerName && p.IsAlive {
				survived++
				break
			}
		}
	}
	return survived
}

// FindTickAt returns the tick closest to (>=) targetTick using binary search.
func FindTickAt(ticks []TickData, targetTick int) *TickData {
	lo, hi := 0, len(ticks)-1
	for lo < hi {
		mid := (lo + hi) / 2
		if ticks[mid].Tick < targetTick {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(ticks) {
		return &ticks[lo]
	}
	return nil
}
