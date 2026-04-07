package stats

import "math"

// HLTV Rating 3.0 approximation weights.
const (
	wKills      = 0.20
	wDamage     = 0.15
	wRoundSwing = 0.25
	wSurvival   = 0.15
	wKAST       = 0.15
	wMultiKills = 0.10

	avgKPR       = 0.68
	avgADR       = 80.0
	avgSurvival  = 0.32
	avgKAST      = 70.0
	avgMultiKill = 0.13
)

// ComputeHLTVRating computes the HLTV Rating 3.0 approximation for a single player.
func ComputeHLTVRating(
	playerName, playerTeam string,
	kills []KillEvent, ticks []TickData, boundaries []RoundBoundary,
	totalRounds int,
	rawKills, rawDeaths int,
	adr, kastPercent float64,
) float64 {
	if totalRounds == 0 {
		return 0
	}

	killsByRound := GroupKillsByRound(kills, boundaries)
	roundSwing := computeRoundSwing(playerName, playerTeam, ticks, boundaries, killsByRound)
	ecoKills := computeEcoKills(playerName, ticks, boundaries, killsByRound)
	multiKillRate := computeMultiKillRate(playerName, killsByRound, totalRounds)

	killRating := (ecoKills / float64(totalRounds)) / avgKPR
	damageRating := adr / avgADR
	survivalRating := (float64(totalRounds-rawDeaths) / float64(totalRounds)) / avgSurvival
	kastRating := kastPercent / avgKAST
	multiKillRating := multiKillRate / avgMultiKill
	roundSwingRating := 1.0 + roundSwing*10

	rating := wKills*killRating +
		wDamage*damageRating +
		wRoundSwing*roundSwingRating +
		wSurvival*survivalRating +
		wKAST*kastRating +
		wMultiKills*multiKillRating

	return math.Round(rating*100) / 100
}

func computeRoundSwing(playerName, playerTeam string, ticks []TickData, boundaries []RoundBoundary, killsByRound map[int][]KillEvent) float64 {
	totalSwing := 0.0

	for _, b := range boundaries {
		roundKills := killsByRound[b.RoundNumber]
		if len(roundKills) == 0 {
			continue
		}

		ctAlive, tAlive := getInitialAlive(ticks, b)
		bombPlanted := false
		bombPlantTick := getBombPlantTick(ticks, b)

		sorted := make([]KillEvent, len(roundKills))
		copy(sorted, roundKills)
		SortKillsByTick(sorted)

		for _, kill := range sorted {
			if bombPlantTick > 0 && kill.Tick >= bombPlantTick {
				bombPlanted = true
			}

			pBefore := GetWinProbCT(ctAlive, tAlive, bombPlanted)

			if kill.VictimTeam == "CT" {
				ctAlive = max(0, ctAlive-1)
			} else if kill.VictimTeam == "T" {
				tAlive = max(0, tAlive-1)
			}

			pAfter := GetWinProbCT(ctAlive, tAlive, bombPlanted)

			var delta float64
			if kill.KillerTeam == "CT" {
				delta = pAfter - pBefore
			} else {
				delta = pBefore - pAfter
			}

			// Credit: killer 70%, flash assist 15%, victim penalty 70%
			if kill.KillerName == playerName {
				totalSwing += delta * 0.70
			}
			if kill.AssisterName == playerName && kill.IsAssistedFlash {
				totalSwing += delta * 0.15
			}
			if kill.VictimName == playerName {
				totalSwing -= delta * 0.70
			}
		}
	}

	if len(boundaries) > 0 {
		return totalSwing / float64(len(boundaries))
	}
	return 0
}

func computeEcoKills(playerName string, ticks []TickData, boundaries []RoundBoundary, killsByRound map[int][]KillEvent) float64 {
	total := 0.0

	for _, b := range boundaries {
		roundKills := killsByRound[b.RoundNumber]
		equipMap := getEquipmentAtRoundStart(ticks, b)

		for _, k := range roundKills {
			if k.KillerName != playerName {
				continue
			}
			killerTier := TierRifleT1 // default
			victimTier := TierRifleT1
			if t, ok := equipMap[k.KillerName]; ok {
				killerTier = t
			}
			if t, ok := equipMap[k.VictimName]; ok {
				victimTier = t
			}
			total += GetEcoKillWeight(killerTier, victimTier)
		}
	}

	return total
}

func computeMultiKillRate(playerName string, killsByRound map[int][]KillEvent, totalRounds int) float64 {
	if totalRounds == 0 {
		return 0
	}
	multiKillRounds := 0
	for _, roundKills := range killsByRound {
		count := 0
		for _, k := range roundKills {
			if k.KillerName == playerName {
				count++
			}
		}
		if count >= 2 {
			multiKillRounds++
		}
	}
	return float64(multiKillRounds) / float64(totalRounds)
}

func getInitialAlive(ticks []TickData, b RoundBoundary) (int, int) {
	for i := range ticks {
		t := &ticks[i]
		if t.Tick >= b.StartTick && t.Tick <= b.StartTick+16 {
			ct, tt := 0, 0
			for _, p := range t.Players {
				if p.IsAlive {
					if p.Team == "CT" {
						ct++
					} else if p.Team == "T" {
						tt++
					}
				}
			}
			return ct, tt
		}
	}
	return 5, 5
}

func getBombPlantTick(ticks []TickData, b RoundBoundary) int {
	for i := range ticks {
		t := &ticks[i]
		if t.Tick < b.StartTick {
			continue
		}
		if t.Tick > b.EndTick {
			break
		}
		if t.BombPlantTick != nil && *t.BombPlantTick >= b.StartTick {
			return *t.BombPlantTick
		}
	}
	return 0
}

func getEquipmentAtRoundStart(ticks []TickData, b RoundBoundary) map[string]EcoTier {
	m := make(map[string]EcoTier)
	for i := range ticks {
		t := &ticks[i]
		if t.Tick >= b.StartTick && t.Tick <= b.StartTick+16 {
			for _, p := range t.Players {
				m[p.Name] = ClassifyEquipment(p.Equipment)
			}
			return m
		}
	}
	return m
}
