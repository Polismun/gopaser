package stats

import (
	"math"
	"time"
)

// ComputeAllPlayerStats computes all stats from a ParseResult.
func ComputeAllPlayerStats(pr *ParseResult, demoID string) *DemoStatsResult {
	boundaries := BuildRoundBoundaries(pr.Ticks)
	totalRounds := len(boundaries)

	// Pre-compute round groupings once (used by multiple stat functions)
	killsByRound := GroupKillsByRound(pr.Kills, boundaries)
	damagesByRound := GroupDamagesByRound(pr.Damages, boundaries)

	players := make([]PlayerGameStats, 0, len(pr.Stats))

	for _, s := range pr.Stats {
		damage := ComputeDamageStats(pr.Damages, boundaries, damagesByRound, s.Name)
		kill := ComputeKillStats(pr.Kills, boundaries, killsByRound, s.Name)
		clutch := ComputeClutchStats(pr.Kills, pr.Ticks, boundaries, killsByRound, s.Name, s.Team)
		kast := ComputeKAST(pr.Kills, pr.Ticks, boundaries, killsByRound, s.Name)
		utility := ComputeUtilityStats(pr.GrenadeEvents, s.Name)
		duels := ComputeDuels(pr.Kills, s.Name)
		bomb := ComputeBombStats(pr.Ticks, boundaries, s.Name)
		survived := ComputeRoundsSurvived(pr.Ticks, boundaries, s.Name)
		weaponCats := ComputeWeaponCategoryKills(pr.Kills, s.Name)
		economy := ComputeRoundEconomy(pr.Ticks, boundaries, s.Name)

		hltvRating := ComputeHLTVRating(
			s.Name, s.Team,
			pr.Kills, pr.Ticks, boundaries, killsByRound,
			totalRounds,
			s.Kills, s.Deaths,
			damage.ADR, kast,
		)

		kdRatio := 0.0
		if s.Deaths > 0 {
			kdRatio = math.Round(float64(s.Kills)/float64(s.Deaths)*100) / 100
		} else if s.Kills > 0 {
			kdRatio = float64(s.Kills)
		}

		players = append(players, PlayerGameStats{
			Name:             s.Name,
			Team:             s.Team,
			SteamID:          s.SteamID,
			Kills:            s.Kills,
			Deaths:           s.Deaths,
			Assists:          s.Assists,
			KDRatio:          kdRatio,
			ADR:              damage.ADR,
			HSPercent:        kill.HSPercent,
			TotalDamage:      damage.TotalDamage,
			HSKills:          kill.HSKills,
			OpeningKills:     kill.OpeningKills,
			OpeningDeaths:    kill.OpeningDeaths,
			ClutchWins:       clutch.ClutchWins,
			ClutchAttempts:   clutch.ClutchAttempts,
			ClutchBreakdown:  clutch.ClutchBreakdown,
			ClutchEvents:     clutch.ClutchEvents,
			TradeKills:       kill.TradeKills,
			TradedDeaths:     kill.TradedDeaths,
			MultiKillRounds:  kill.MultiKillRounds,
			Duels:            duels,
			BombPlants:       bomb.Plants,
			BombDefuses:      bomb.Defuses,
			RoundsSurvived:   survived,
			WeaponCatKills:   weaponCats,
			FlashAssists:     kill.FlashAssists,
			RoundEconomy:     economy,
			KASTPercent:      kast,
			UtilityThrown:    utility,
			WeaponKills:      kill.WeaponKills,
			DamageByHitgroup: damage.DamageByHitgroup,
			HLTVRating:       hltvRating,
		})
	}

	// Per-round stats (winner, kill count, MVP)
	// killsByRound and damagesByRound already computed above

	rounds := make([]RoundStats, 0, len(boundaries))
	for _, b := range boundaries {
		roundKills := killsByRound[b.RoundNumber]
		roundDamages := damagesByRound[b.RoundNumber]

		// MVP: (kills * 2) + (damage / 50)
		impact := make(map[string]float64)
		for _, k := range roundKills {
			if k.KillerName != "" {
				impact[k.KillerName] += 2
			}
		}
		for _, d := range roundDamages {
			if d.AttackerName != "" {
				impact[d.AttackerName] += float64(d.Damage) / 50
			}
		}

		mvp := ""
		maxImpact := 0.0
		for name, score := range impact {
			if score > maxImpact {
				maxImpact = score
				mvp = name
			}
		}

		rounds = append(rounds, RoundStats{
			Round:     b.RoundNumber,
			Winner:    b.Winner,
			KillCount: len(roundKills),
			MVP:       mvp,
		})
	}

	return &DemoStatsResult{
		Players:     players,
		Rounds:      rounds,
		TotalRounds: totalRounds,
		MapName:     pr.MapName,
		DemoID:      demoID,
		Date:        time.Now().UTC().Format(time.RFC3339),
	}
}
