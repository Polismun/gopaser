package stats

import "strings"

// EcoTier represents a player's equipment tier for eco-adjustment.
type EcoTier int

const (
	TierStarter EcoTier = iota
	TierUpgradedPistol
	TierSMG
	TierRifleT2
	TierRifleT1
	TierSniper
)

var weaponTier = map[string]EcoTier{
	// Starter pistols
	"glock": TierStarter, "usp_silencer": TierStarter, "hkp2000": TierStarter,
	// Upgraded pistols
	"p250": TierUpgradedPistol, "tec9": TierUpgradedPistol, "fiveseven": TierUpgradedPistol,
	"cz75a": TierUpgradedPistol, "deagle": TierUpgradedPistol, "revolver": TierUpgradedPistol,
	"elite": TierUpgradedPistol,
	// SMGs + shotguns
	"mac10": TierSMG, "mp9": TierSMG, "mp7": TierSMG, "mp5sd": TierSMG, "ump45": TierSMG,
	"bizon": TierSMG, "p90": TierSMG,
	"nova": TierSMG, "xm1014": TierSMG, "sawedoff": TierSMG, "mag7": TierSMG,
	// Tier-2 rifles
	"galilar": TierRifleT2, "famas": TierRifleT2, "ssg08": TierRifleT2,
	// Tier-1 rifles + MGs
	"ak47": TierRifleT1, "m4a4": TierRifleT1, "m4a1_silencer": TierRifleT1,
	"sg556": TierRifleT1, "aug": TierRifleT1, "m249": TierRifleT1, "negev": TierRifleT1,
	// Snipers
	"awp": TierSniper, "scar20": TierSniper, "g3sg1": TierSniper,
}

// ClassifyEquipment returns the eco tier based on the best weapon in the loadout.
func ClassifyEquipment(equipment []string) EcoTier {
	best := TierStarter
	for _, w := range equipment {
		if t, ok := weaponTier[strings.ToLower(w)]; ok && t > best {
			best = t
		}
	}
	return best
}

// Duel win-rate table: [killer][victim] → probability killer wins.
// Indexed by EcoTier (0-5).
var duelWinRate = [6][6]float64{
	// killer\victim:  starter  upPist  smg    rifT2  rifT1  sniper
	/* starter   */ {0.50, 0.42, 0.35, 0.30, 0.25, 0.20},
	/* upPistol  */ {0.58, 0.50, 0.42, 0.35, 0.30, 0.25},
	/* smg       */ {0.65, 0.58, 0.50, 0.42, 0.35, 0.30},
	/* rifleT2   */ {0.70, 0.65, 0.58, 0.50, 0.42, 0.35},
	/* rifleT1   */ {0.75, 0.70, 0.65, 0.58, 0.50, 0.42},
	/* sniper    */ {0.80, 0.75, 0.70, 0.65, 0.58, 0.50},
}

// GetDuelWinRate returns the expected win-rate for killer vs victim equipment tiers.
func GetDuelWinRate(killer, victim EcoTier) float64 {
	return duelWinRate[killer][victim]
}

// GetEcoKillWeight returns the eco-adjustment weight for a kill.
// Easy kills → weight < 1, hard kills → weight > 1. 50/50 = 1.0.
func GetEcoKillWeight(killer, victim EcoTier) float64 {
	wr := GetDuelWinRate(killer, victim)
	if wr < 0.10 {
		wr = 0.10
	}
	return 0.50 / wr
}
