package stats

import "strings"

// Weapon category classification for CS2.
var weaponCategories = map[string]string{
	// Pistols
	"glock":          "pistol",
	"hkp2000":        "pistol",
	"usp_silencer":   "pistol",
	"p250":           "pistol",
	"fiveseven":      "pistol",
	"tec9":           "pistol",
	"cz75a":          "pistol",
	"deagle":         "pistol",
	"revolver":       "pistol",
	"elite":          "pistol",
	// SMGs
	"mac10":          "smg",
	"mp9":            "smg",
	"mp7":            "smg",
	"mp5sd":          "smg",
	"ump45":          "smg",
	"p90":            "smg",
	"bizon":          "smg",
	// Rifles
	"galilar":        "rifle",
	"famas":          "rifle",
	"ak47":           "rifle",
	"m4a1":           "rifle",
	"m4a1_silencer":  "rifle",
	"sg556":          "rifle",
	"aug":            "rifle",
	// Snipers
	"ssg08":          "sniper",
	"awp":            "sniper",
	"scar20":         "sniper",
	"g3sg1":          "sniper",
	// Heavy
	"nova":           "heavy",
	"xm1014":         "heavy",
	"sawedoff":       "heavy",
	"mag7":           "heavy",
	"m249":           "heavy",
	"negev":          "heavy",
	// Other
	"knife":          "melee",
	"knife_t":        "melee",
	"bayonet":        "melee",
	"taser":          "equipment",
	"hegrenade":      "grenade",
	"molotov":        "grenade",
	"incgrenade":     "grenade",
	"inferno":        "grenade",
}

// ClassifyWeapon returns the category for a weapon name.
func ClassifyWeapon(weapon string) string {
	w := strings.ToLower(weapon)
	if cat, ok := weaponCategories[w]; ok {
		return cat
	}
	// Fallback: check if it contains "knife" (skin variants)
	if strings.Contains(w, "knife") || strings.Contains(w, "bayonet") || strings.Contains(w, "karambit") ||
		strings.Contains(w, "butterfly") || strings.Contains(w, "falchion") || strings.Contains(w, "bowie") ||
		strings.Contains(w, "navaja") || strings.Contains(w, "stiletto") || strings.Contains(w, "talon") ||
		strings.Contains(w, "ursus") || strings.Contains(w, "skeleton") || strings.Contains(w, "nomad") ||
		strings.Contains(w, "paracord") || strings.Contains(w, "survival") || strings.Contains(w, "classic") ||
		strings.Contains(w, "flip") || strings.Contains(w, "gut") || strings.Contains(w, "huntsman") ||
		strings.Contains(w, "kukri") {
		return "melee"
	}
	return "other"
}

// ComputeWeaponCategoryKills counts kills per weapon category.
func ComputeWeaponCategoryKills(kills []KillEvent, playerName string) map[string]int {
	cats := make(map[string]int)
	for _, k := range kills {
		if k.KillerName == playerName && k.Weapon != "" {
			cat := ClassifyWeapon(k.Weapon)
			cats[cat]++
		}
	}
	return cats
}
