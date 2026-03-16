package main

// appendOrMergeTick garantit des ticks strictement croissants dans le slice.
//   - tick > dernier  → append normal.
//   - tick == dernier → merge (OR flags, préférer players non-vide, propager roundNumber/winner).
//   - tick < dernier  → ignore (ne devrait pas arriver en pratique).
func appendOrMergeTick(ticks *[]TickData, t TickData) {
	if len(*ticks) == 0 {
		*ticks = append(*ticks, t)
		return
	}
	last := &(*ticks)[len(*ticks)-1]
	if t.Tick > last.Tick {
		*ticks = append(*ticks, t)
	} else if t.Tick == last.Tick {
		last.IsRoundStart  = last.IsRoundStart  || t.IsRoundStart
		last.IsRoundEnd    = last.IsRoundEnd    || t.IsRoundEnd
		last.IsFreezeStart = last.IsFreezeStart || t.IsFreezeStart
		if len(t.Players) > 0 {
			last.Players = t.Players
		}
		if t.RoundNumber > 0 && last.RoundNumber == 0 {
			last.RoundNumber = t.RoundNumber
		}
		if t.Winner != "" {
			last.Winner = t.Winner
		}
		if len(t.Projectiles) > 0 {
			last.Projectiles = t.Projectiles
		}
		if t.BombPlantTick != 0 {
			last.BombPlantTick = t.BombPlantTick
		}
		if t.TeamCT != "" && last.TeamCT == "" {
			last.TeamCT = t.TeamCT
		}
		if t.TeamT != "" && last.TeamT == "" {
			last.TeamT = t.TeamT
		}
	}
	// t.Tick < last.Tick → ignore
}
