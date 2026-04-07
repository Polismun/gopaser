package stats

// PlayerGameStats holds computed stats for a single player in a match.
type PlayerGameStats struct {
	Name             string                     `json:"name" firestore:"name"`
	Team             string                     `json:"team" firestore:"team"`
	SteamID          string                     `json:"steamId,omitempty" firestore:"steamId,omitempty"`
	Kills            int                        `json:"kills" firestore:"kills"`
	Deaths           int                        `json:"deaths" firestore:"deaths"`
	Assists          int                        `json:"assists" firestore:"assists"`
	KDRatio          float64                    `json:"kdRatio" firestore:"kdRatio"`
	ADR              float64                    `json:"adr" firestore:"adr"`
	HSPercent        float64                    `json:"hsPercent" firestore:"hsPercent"`
	TotalDamage      int                        `json:"totalDamage" firestore:"totalDamage"`
	HSKills          int                        `json:"hsKills" firestore:"hsKills"`
	OpeningKills     int                        `json:"openingKills" firestore:"openingKills"`
	OpeningDeaths    int                        `json:"openingDeaths" firestore:"openingDeaths"`
	ClutchWins       int                        `json:"clutchWins" firestore:"clutchWins"`
	ClutchAttempts   int                        `json:"clutchAttempts" firestore:"clutchAttempts"`
	KASTPercent      float64                    `json:"kastPercent" firestore:"kastPercent"`
	UtilityThrown    UtilityCount               `json:"utilityThrown" firestore:"utilityThrown"`
	WeaponKills      map[string]WeaponKillStats `json:"weaponKills" firestore:"weaponKills"`
	DamageByHitgroup map[string]int             `json:"damageByHitgroup" firestore:"damageByHitgroup"`
	HLTVRating       float64                    `json:"hltvRating" firestore:"hltvRating"`
}

// WeaponKillStats holds per-weapon kill breakdown.
type WeaponKillStats struct {
	Kills   int `json:"kills" firestore:"kills"`
	HSKills int `json:"hsKills" firestore:"hsKills"`
}

// UtilityCount holds grenade throw counts per type.
type UtilityCount struct {
	Smoke   int `json:"smoke" firestore:"smoke"`
	Flash   int `json:"flash" firestore:"flash"`
	HE      int `json:"he" firestore:"he"`
	Molotov int `json:"molotov" firestore:"molotov"`
}

// RoundStats holds per-round summary.
type RoundStats struct {
	Round     int    `json:"round" firestore:"round"`
	Winner    string `json:"winner" firestore:"winner"`
	KillCount int    `json:"killCount" firestore:"killCount"`
	MVP       string `json:"mvp,omitempty" firestore:"mvp,omitempty"`
}

// DemoStatsResult is the top-level stats output stored in Firestore demoStats/{id}.
type DemoStatsResult struct {
	Players     []PlayerGameStats `json:"players" firestore:"players"`
	Rounds      []RoundStats      `json:"rounds" firestore:"rounds"`
	TotalRounds int               `json:"totalRounds" firestore:"totalRounds"`
	MapName     string            `json:"mapName" firestore:"mapName"`
	DemoID      string            `json:"demoId" firestore:"demoId"`
	Date        string            `json:"date" firestore:"date"`
}

// RoundBoundary marks the start/end ticks and winner of a round.
type RoundBoundary struct {
	RoundNumber int
	StartTick   int
	EndTick     int
	Winner      string // "CT", "T", or ""
}

// ParseResult mirrors the JSON output from the Go parser (relevant fields only).
type ParseResult struct {
	Success       bool           `json:"success"`
	MapName       string         `json:"mapName"`
	TickRate      int            `json:"tickRate"`
	Stats         []PlayerStat   `json:"stats"`
	Ticks         []TickData     `json:"ticks"`
	Kills         []KillEvent    `json:"kills"`
	Damages       []DamageEvent  `json:"damages"`
	GrenadeEvents []GrenadeEvent `json:"grenadeEvents"`
}

// PlayerStat is the end-of-match KDA from the parser.
type PlayerStat struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	SteamID  string `json:"steamId,omitempty"`
	Kills    int    `json:"kills"`
	Deaths   int    `json:"deaths"`
	Assists  int    `json:"assists"`
	Rank     int    `json:"rank,omitempty"`
	RankType int    `json:"rankType,omitempty"`
	Wins     int    `json:"wins,omitempty"`
}

// TickData is a per-tick snapshot (only fields needed for stats).
type TickData struct {
	Tick          int          `json:"tick"`
	Players       []TickPlayer `json:"players"`
	IsRoundStart  bool         `json:"isRoundStart,omitempty"`
	IsRoundEnd    bool         `json:"isRoundEnd,omitempty"`
	RoundNumber   *int         `json:"roundNumber,omitempty"`
	Winner        string       `json:"winner,omitempty"`
	BombPlantTick *int         `json:"bombPlantTick,omitempty"`
}

// TickPlayer is a per-tick player state (only fields needed for stats).
type TickPlayer struct {
	Name      string   `json:"name"`
	Team      string   `json:"team"`
	IsAlive   bool     `json:"isAlive"`
	Equipment []string `json:"equipment"`
}

// KillEvent mirrors the parser output.
type KillEvent struct {
	Tick            int    `json:"tick"`
	KillerName      string `json:"killerName"`
	KillerTeam      string `json:"killerTeam"`
	VictimName      string `json:"victimName"`
	VictimTeam      string `json:"victimTeam"`
	Weapon          string `json:"weapon"`
	IsHeadshot      bool   `json:"isHeadshot"`
	AssisterName    string `json:"assisterName"`
	AssisterTeam    string `json:"assisterTeam"`
	IsAssistedFlash bool   `json:"isAssistedFlash,omitempty"`
}

// DamageEvent mirrors the parser output.
type DamageEvent struct {
	Tick         int    `json:"tick"`
	VictimName   string `json:"victimName"`
	AttackerName string `json:"attackerName"`
	Damage       int    `json:"damage"`
	HitGroup     string `json:"hitGroup"`
}

// GrenadeEvent mirrors the parser output (only throw action used).
type GrenadeEvent struct {
	Type        string `json:"type"`
	Action      string `json:"action"`
	ThrowerName string `json:"throwerName"`
}
