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

// GroupKillsByRound assigns kill events to rounds by tick range.
func GroupKillsByRound(kills []KillEvent, boundaries []RoundBoundary) map[int][]KillEvent {
	m := make(map[int][]KillEvent, len(boundaries))
	for _, b := range boundaries {
		m[b.RoundNumber] = nil
	}
	for i := range kills {
		k := &kills[i]
		for _, b := range boundaries {
			if k.Tick >= b.StartTick && k.Tick <= b.EndTick {
				m[b.RoundNumber] = append(m[b.RoundNumber], *k)
				break
			}
		}
	}
	return m
}

// GroupDamagesByRound assigns damage events to rounds by tick range.
func GroupDamagesByRound(damages []DamageEvent, boundaries []RoundBoundary) map[int][]DamageEvent {
	m := make(map[int][]DamageEvent, len(boundaries))
	for _, b := range boundaries {
		m[b.RoundNumber] = nil
	}
	for i := range damages {
		d := &damages[i]
		for _, b := range boundaries {
			if d.Tick >= b.StartTick && d.Tick <= b.EndTick {
				m[b.RoundNumber] = append(m[b.RoundNumber], *d)
				break
			}
		}
	}
	return m
}

// SortKillsByTick sorts a slice of kills chronologically.
func SortKillsByTick(kills []KillEvent) {
	sort.Slice(kills, func(i, j int) bool { return kills[i].Tick < kills[j].Tick })
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
