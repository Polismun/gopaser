package main

// PlayerStats holds end-of-game KDA for a player.
type PlayerStats struct {
	Name     string `json:"name"`
	Team     string `json:"team"`
	SteamID  uint64 `json:"steamId,omitempty,string"`
	Kills    int    `json:"kills"`
	Deaths   int    `json:"deaths"`
	Assists  int    `json:"assists"`
	Rank     int    `json:"rank,omitempty"`
	RankType int    `json:"rankType,omitempty"`
	Wins     int    `json:"wins,omitempty"`
}

// PlayerPosition is a per-tick snapshot of a player's state.
type PlayerPosition struct {
	Name         string   `json:"name"`
	Team         string   `json:"team"`
	X            float64  `json:"x"`
	Y            float64  `json:"y"`
	Z            float64  `json:"z"`
	Yaw          float64  `json:"yaw"`
	Health       int      `json:"health"`
	IsAlive      bool     `json:"isAlive"`
	ActiveWeapon string   `json:"activeWeapon"`
	Money        int      `json:"money"`
	Armor        int      `json:"armor"`
	HasHelmet    bool     `json:"hasHelmet"`
	HasDefuseKit  bool     `json:"hasDefuseKit"`
	Equipment     []string `json:"equipment"`
	FlashDuration float64  `json:"flashDuration,omitempty"`
	IsScoped      bool     `json:"isScoped,omitempty"`
}

// ShotEvent is emitted on WeaponFire (muzzle flash rendering).
type ShotEvent struct {
	Tick    int     `json:"tick"`
	Shooter string  `json:"shooter"`
	Team    string  `json:"team"`
	X       float64 `json:"x"`
	Y       float64 `json:"y"`
	Yaw     float64 `json:"yaw"`
	Weapon  string  `json:"weapon"`
}

// DamageEvent is emitted on PlayerHurt (hit flash rendering).
type DamageEvent struct {
	Tick         int     `json:"tick"`
	VictimName   string  `json:"victimName"`
	AttackerName string  `json:"attackerName"`
	Damage       int     `json:"damage"`
	HealthAfter  int     `json:"healthAfter"`
	HitGroup     string  `json:"hitGroup"`
	VictimX      float64 `json:"victimX"`
	VictimY      float64 `json:"victimY"`
}

// KillEvent is emitted on Kill (killfeed).
type KillEvent struct {
	Tick          int    `json:"tick"`
	KillerName    string `json:"killerName"`
	KillerTeam    string `json:"killerTeam"`
	VictimName    string `json:"victimName"`
	VictimTeam    string `json:"victimTeam"`
	Weapon        string `json:"weapon"`
	IsHeadshot    bool   `json:"isHeadshot"`
	IsWallbang    bool   `json:"isWallbang"`
	IsSmokeKill   bool   `json:"isSmokeKill"`
	IsAttackerBlind bool  `json:"isAttackerBlind,omitempty"`
	IsAssistedFlash bool  `json:"isAssistedFlash,omitempty"`
	IsNoScope      bool   `json:"isNoScope"`
	AssisterName  string `json:"assisterName"`
	AssisterTeam  string `json:"assisterTeam"`
}

// GrenadeEvent is emitted on grenade lifecycle events (throw/start/expired/detonate).
type GrenadeEvent struct {
	Type         string  `json:"type"`
	Action       string  `json:"action"`
	Tick         int     `json:"tick"`
	EntityID     int     `json:"entityId"`
	ThrowerName  string  `json:"throwerName"`
	ThrowerTeam  string  `json:"throwerTeam"`
	X            float64 `json:"x"`
	Y            float64 `json:"y"`
	Z            float64 `json:"z"`
	ThrowerX     float64 `json:"throwerX,omitempty"`
	ThrowerY     float64 `json:"throwerY,omitempty"`
	ThrowerZ     float64 `json:"throwerZ,omitempty"`
	ThrowerYaw      float64 `json:"throwerYaw,omitempty"`
	ThrowerPitch    float64 `json:"throwerPitch,omitempty"`
	ThrowerAirborne  bool    `json:"throwerAirborne,omitempty"`
	ThrowerCrouching bool    `json:"throwerCrouching,omitempty"`
	ThrowerSpeed     float64 `json:"throwerSpeed,omitempty"`
	ProjectileSpeed  float64 `json:"projectileSpeed,omitempty"`
	ThrowerAttack    bool    `json:"throwerAttack,omitempty"`
	ThrowerAttack2   bool    `json:"throwerAttack2,omitempty"`
}

// ProjectilePos is a per-tick snapshot of an in-flight grenade projectile.
type ProjectilePos struct {
	EntityID    int     `json:"entityId"`
	Type        string  `json:"type"`
	ThrowerName string  `json:"throwerName"`
	ThrowerTeam string  `json:"throwerTeam"`
	X           float64 `json:"x"`
	Y           float64 `json:"y"`
	Z           float64 `json:"z"`
}

// DroppedItem is a per-tick snapshot of a weapon/grenade lying on the ground.
type DroppedItem struct {
	EntityID int     `json:"entityId"`
	Weapon   string  `json:"weapon"`
	X        float64 `json:"x"`
	Y        float64 `json:"y"`
	Z        float64 `json:"z"`
}

// TickData is one downsampled tick of game state.
type TickData struct {
	Tick               int              `json:"tick"`
	Players            []PlayerPosition `json:"players"`
	Projectiles        []ProjectilePos  `json:"projectiles,omitempty"`
	DroppedItems       []DroppedItem    `json:"droppedItems,omitempty"`
	IsRoundStart       bool             `json:"isRoundStart,omitempty"`
	IsRoundEnd         bool             `json:"isRoundEnd,omitempty"`
	IsFreezeStart      bool             `json:"isFreezeStart,omitempty"`
	RoundNumber        int              `json:"roundNumber,omitempty"`
	Winner             string           `json:"winner,omitempty"`
	TeamCT             string           `json:"teamCT,omitempty"`
	TeamT              string           `json:"teamT,omitempty"`
	BombPlantTick      int              `json:"bombPlantTick,omitempty"`
	BombPlantX         float64          `json:"bombPlantX"`
	BombPlantY         float64          `json:"bombPlantY"`
	BombPlantZ         float64          `json:"bombPlantZ"`
	IsPlanting         bool             `json:"isPlanting,omitempty"`
	IsDefusing         bool             `json:"isDefusing,omitempty"`
	PlantingPlayerName string           `json:"plantingPlayerName,omitempty"`
	DefusingPlayerName string           `json:"defusingPlayerName,omitempty"`
	ScoreCT            int              `json:"scoreCT,omitempty"`
	ScoreT             int              `json:"scoreT,omitempty"`
}

// ParseResult holds the scalar fields of the output JSON.
// Large arrays (ticks, shots, damages, grenadeEvents, kills) are written
// via jsonlSpiller temp files and streamed directly to stdout to avoid
// holding everything in memory during parsing.
type ParseResult struct {
	Success    bool          `json:"success"`
	Error      string        `json:"error,omitempty"`
	MapName    string        `json:"mapName"`
	ServerName string        `json:"serverName,omitempty"`
	TickRate   int           `json:"tickRate"`
	Stats      []PlayerStats `json:"stats"`
}
