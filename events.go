package main

import (
	"math"

	common "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/common"
	dem "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs"
	events "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/events"
)

// bombState holds all mutable bomb/plant/defuse state shared across event handlers.
type bombState struct {
	plantTick      int
	plantX, plantY float64
	isPlanting     bool
	isDefusing     bool
	plantingPlayer string
	defusingPlayer string
	plantBeginTick int     // tick when BombPlantBegin fired, for fallback detection
	plantBeginX    float64 // planting player X at BombPlantBegin, for fallback position
	plantBeginY    float64 // planting player Y at BombPlantBegin, for fallback position
}

// skipRoundState tracks knife/phantom round skipping.
type skipRoundState struct {
	active          bool      // true during a skipped round (knife)
	isKnifeSkip     bool      // true if the current skip is for the knife round
	phantomPossible bool      // true after knife round ends: next round might be phantom
	inFreeze        bool      // true between RoundStart and RoundFreezetimeEnd (skip FrameDone)
	pendingFreeze   *TickData // buffered freeze tick from RoundStart
	// Snapshots for retroactive phantom purge
	tickSnapshot        int
	shotSnapshot        int
	damageSnapshot      int
	killSnapshot        int
	grenadeSnapshot     int
	roundNumberSnapshot int
}

// isKnifeRound checks if all alive players have only melee weapons.
func isKnifeRound(gs dem.GameState) bool {
	for _, player := range gs.Participants().Playing() {
		if player == nil || !player.IsAlive() {
			continue
		}
		for _, w := range player.Weapons() {
			if w == nil {
				continue
			}
			if w.Type != common.EqKnife {
				return false
			}
		}
	}
	return true
}

// registerFrameHandler installs the FrameDone downsampling handler.
func registerFrameHandler(p dem.Parser, result *ParseResult, bs *bombState, srs *skipRoundState) {
	const downsampleRate = 8
	lastDownsampleTick := -1

	p.RegisterEventHandler(func(e events.FrameDone) {
		gs := p.GameState()

		if result.MapName == "" || result.MapName == "unknown" {
			if h := p.Header(); h.MapName != "" && h.MapName != "unknown" {
				result.MapName = h.MapName
			}
		}

		if srs.active || srs.inFreeze {
			return
		}

		currentTick := gs.IngameTick()
		if currentTick%downsampleRate != 0 {
			return
		}
		if currentTick == lastDownsampleTick {
			return
		}
		lastDownsampleTick = currentTick

		// Fallback: detect bomb planted from state transition if BombPlanted event was missed
		if bs.plantTick == 0 && bs.plantBeginTick > 0 && !bs.isPlanting {
			// BombPlantBegin fired, isPlanting went false, but plantTick never set
			// Estimate plant completion tick (~3.2s after begin)
			tickRate := p.TickRate()
			if tickRate <= 0 {
				tickRate = 64
			}
			bs.plantTick = bs.plantBeginTick + int(3.2*tickRate)
			bs.plantBeginTick = 0
			bomb := gs.Bomb()
			if bomb != nil {
				pos := bomb.Position()
				bs.plantX = float64(pos.X)
				bs.plantY = float64(pos.Y)
			}
			// Fallback position from planting player
			if bs.plantX == 0 && bs.plantY == 0 {
				bs.plantX = bs.plantBeginX
				bs.plantY = bs.plantBeginY
			}
		}

		tickEntry := TickData{
			Tick:               currentTick,
			Players:            []PlayerPosition{},
			TeamCT:             gs.TeamCounterTerrorists().ClanName(),
			TeamT:              gs.TeamTerrorists().ClanName(),
			BombPlantTick:      bs.plantTick,
			BombPlantX:         bs.plantX,
			BombPlantY:         bs.plantY,
			IsPlanting:         bs.isPlanting,
			IsDefusing:         bs.isDefusing,
			PlantingPlayerName: bs.plantingPlayer,
			DefusingPlayerName: bs.defusingPlayer,
			ScoreCT:            gs.TeamCounterTerrorists().Score(),
			ScoreT:             gs.TeamTerrorists().Score(),
		}

		for _, player := range gs.TeamCounterTerrorists().Members() {
			if player != nil {
				tickEntry.Players = append(tickEntry.Players, playerToPosition(player, "CT"))
			}
		}
		for _, player := range gs.TeamTerrorists().Members() {
			if player != nil {
				tickEntry.Players = append(tickEntry.Players, playerToPosition(player, "T"))
			}
		}

		for _, proj := range gs.GrenadeProjectiles() {
			if proj == nil {
				continue
			}
			pos := proj.Position()
			grenType := "unknown"
			if proj.WeaponInstance != nil {
				grenType = grenadeTypeString(proj.WeaponInstance.Type)
			}
			throwerName, throwerTeam := "", ""
			if proj.Thrower != nil {
				throwerName = proj.Thrower.Name
				throwerTeam = playerTeamString(proj.Thrower.Team)
			}
			tickEntry.Projectiles = append(tickEntry.Projectiles, ProjectilePos{
				EntityID:    int(proj.Entity.ID()),
				Type:        grenType,
				ThrowerName: throwerName,
				ThrowerTeam: throwerTeam,
				X:           pos.X,
				Y:           pos.Y,
				Z:           pos.Z,
			})
		}

		tickEntry.DroppedItems = collectDroppedItems(gs)

		if len(tickEntry.Players) > 0 || len(tickEntry.Projectiles) > 0 {
			appendOrMergeTick(&result.Ticks, tickEntry)
		}
	})
}

// registerEventHandlers installs all non-frame event handlers.
func registerEventHandlers(p dem.Parser, result *ParseResult, bs *bombState, roundNumber *int, srs *skipRoundState) {
	// WeaponFire — muzzle flash
	p.RegisterEventHandler(func(e events.WeaponFire) {
		if srs.active {
			return
		}
		if e.Shooter == nil {
			return
		}
		if e.Weapon != nil && (e.Weapon.Class() == common.EqClassEquipment || e.Weapon.Class() == common.EqClassGrenade) {
			return
		}
		pos := e.Shooter.Position()
		weapon := ""
		if e.Weapon != nil {
			weapon = e.Weapon.String()
		}
		result.Shots = append(result.Shots, ShotEvent{
			Tick:    p.GameState().IngameTick(),
			Shooter: e.Shooter.Name,
			Team:    playerTeamString(e.Shooter.Team),
			X:       pos.X,
			Y:       pos.Y,
			Yaw:     float64(e.Shooter.ViewDirectionX()),
			Weapon:  weapon,
		})
	})

	// PlayerHurt — hit flash
	p.RegisterEventHandler(func(e events.PlayerHurt) {
		if srs.active {
			return
		}
		if e.Player == nil {
			return
		}
		victimPos := e.Player.Position()
		attackerName := ""
		if e.Attacker != nil {
			attackerName = e.Attacker.Name
		}
		result.Damages = append(result.Damages, DamageEvent{
			Tick:         p.GameState().IngameTick(),
			VictimName:   e.Player.Name,
			AttackerName: attackerName,
			Damage:       e.HealthDamageTaken,
			HealthAfter:  e.Health,
			HitGroup:     hitGroupString(e.HitGroup),
			VictimX:      victimPos.X,
			VictimY:      victimPos.Y,
		})
	})

	// Kill — killfeed
	p.RegisterEventHandler(func(e events.Kill) {
		if srs.active {
			return
		}
		if e.Victim == nil {
			return
		}
		killerName, killerTeam := "", ""
		if e.Killer != nil {
			killerName = e.Killer.Name
			killerTeam = playerTeamString(e.Killer.Team)
		}
		weapon := ""
		if e.Weapon != nil {
			weapon = e.Weapon.String()
		}
		assisterName, assisterTeam := "", ""
		if e.Assister != nil {
			assisterName = e.Assister.Name
			assisterTeam = playerTeamString(e.Assister.Team)
		}
		result.Kills = append(result.Kills, KillEvent{
			Tick:          p.GameState().IngameTick(),
			KillerName:    killerName,
			KillerTeam:    killerTeam,
			VictimName:    e.Victim.Name,
			VictimTeam:    playerTeamString(e.Victim.Team),
			Weapon:        weapon,
			IsHeadshot:    e.IsHeadshot,
			IsWallbang:    e.IsWallBang(),
			IsSmokeKill:   e.ThroughSmoke,
			IsAttackerBlind: e.AttackerBlind,
			IsAssistedFlash: e.AssistedFlash,
			IsNoScope:      e.NoScope,
			AssisterName:  assisterName,
			AssisterTeam:  assisterTeam,
		})
		// Safety net: clear planting/defusing state if victim was the acting player
		if bs.isPlanting && e.Victim.Name == bs.plantingPlayer {
			bs.isPlanting = false
			bs.plantingPlayer = ""
			bs.plantBeginTick = 0
			bs.plantBeginX = 0
			bs.plantBeginY = 0
		}
		if bs.isDefusing && e.Victim.Name == bs.defusingPlayer {
			bs.isDefusing = false
			bs.defusingPlayer = ""
		}
	})

	// Track the last tick where each attack button was pressed, per player.
	// The throw animation has a delay (~10-25 ticks) between button release and
	// projectile creation, so we can't just check the current or previous tick.
	// Instead we check if the button was pressed within a recent window.
	type buttonLastPress struct {
		attackTick  int
		attack2Tick int
	}
	attackHistory := make(map[uint64]*buttonLastPress) // steamID64 -> last press ticks
	p.RegisterEventHandler(func(e events.FrameDone) {
		tick := p.GameState().IngameTick()
		for _, pl := range p.GameState().Participants().Playing() {
			h, ok := attackHistory[pl.SteamID64]
			if !ok {
				h = &buttonLastPress{}
				attackHistory[pl.SteamID64] = h
			}
			if pl.IsPressingButton(common.ButtonAttack) {
				h.attackTick = tick
			}
			if pl.IsPressingButton(common.ButtonAttack2) {
				h.attack2Tick = tick
			}
		}
	})

	// Grenade throw — captures thrower position & view angles for lineup reproduction
	p.RegisterEventHandler(func(e events.GrenadeProjectileThrow) {
		if srs.active {
			return
		}
		proj := e.Projectile
		if proj == nil {
			return
		}
		throwerName, throwerTeam := "", ""
		var throwerX, throwerY, throwerZ, throwerYaw, throwerPitch, throwerSpeed, projectileSpeed float64
		var throwerAirborne, throwerCrouching, throwerAttack, throwerAttack2 bool
		if proj.Thrower != nil {
			throwerName = proj.Thrower.Name
			throwerTeam = playerTeamString(proj.Thrower.Team)
			pos := proj.Thrower.Position()
			throwerX = pos.X
			throwerY = pos.Y
			throwerZ = pos.Z
			throwerYaw = float64(proj.Thrower.ViewDirectionX())
			throwerPitch = float64(proj.Thrower.ViewDirectionY())
			vel := proj.Thrower.Velocity()
			throwerSpeed = math.Sqrt(vel.X*vel.X + vel.Y*vel.Y)
			// ButtonJump: hint only — TS does parabola check on Z positions for reliable detection
			throwerAirborne = proj.Thrower.IsPressingButton(common.ButtonJump)
			throwerCrouching = proj.Thrower.IsDucking()
			// Check if attack buttons were pressed within a recent window (~64 ticks).
			// The throw animation delays projectile creation by ~10-25 ticks after button release.
			if h, ok := attackHistory[proj.Thrower.SteamID64]; ok {
				tick := p.GameState().IngameTick()
				throwerAttack = (tick - h.attackTick) < 64
				throwerAttack2 = (tick - h.attack2Tick) < 64
			}
			// Pure throw velocity = projectile velocity minus player velocity
			// Used to classify throw strength (left-click / right-click / both)
			// Use PropertyValue (not PropertyValueMust) to avoid panic if property is missing
			if projVelProp, exists := proj.Entity.PropertyValue("m_vecVelocity"); exists {
				projVel := projVelProp.VectorVal
				dx := projVel.X - vel.X
				dy := projVel.Y - vel.Y
				dz := projVel.Z - vel.Z
				projectileSpeed = math.Sqrt(dx*dx + dy*dy + dz*dz)
			}
		}
		grenType := "unknown"
		if proj.WeaponInstance != nil {
			grenType = grenadeTypeString(proj.WeaponInstance.Type)
		}
		projPos := proj.Position()
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: grenType, Action: "throw", Tick: p.GameState().IngameTick(),
			EntityID: int(proj.Entity.ID()), ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: projPos.X, Y: projPos.Y, Z: projPos.Z,
			ThrowerX: throwerX, ThrowerY: throwerY, ThrowerZ: throwerZ,
			ThrowerYaw: throwerYaw, ThrowerPitch: throwerPitch,
			ThrowerAirborne: throwerAirborne, ThrowerCrouching: throwerCrouching, ThrowerSpeed: throwerSpeed,
			ProjectileSpeed: projectileSpeed, ThrowerAttack: throwerAttack, ThrowerAttack2: throwerAttack2,
		})
	})

	// Grenade lifecycle — smoke
	p.RegisterEventHandler(func(e events.SmokeStart) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		if e.Thrower != nil {
			throwerName = e.Thrower.Name
			throwerTeam = playerTeamString(e.Thrower.Team)
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: "smoke", Action: "start", Tick: p.GameState().IngameTick(),
			EntityID: e.GrenadeEntityID, ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: e.Position.X, Y: e.Position.Y, Z: e.Position.Z,
		})
	})

	p.RegisterEventHandler(func(e events.SmokeExpired) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		if e.Thrower != nil {
			throwerName = e.Thrower.Name
			throwerTeam = playerTeamString(e.Thrower.Team)
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: "smoke", Action: "expired", Tick: p.GameState().IngameTick(),
			EntityID: e.GrenadeEntityID, ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: e.Position.X, Y: e.Position.Y, Z: e.Position.Z,
		})
	})

	// HE
	p.RegisterEventHandler(func(e events.HeExplode) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		if e.Thrower != nil {
			throwerName = e.Thrower.Name
			throwerTeam = playerTeamString(e.Thrower.Team)
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: "he", Action: "detonate", Tick: p.GameState().IngameTick(),
			EntityID: e.GrenadeEntityID, ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: e.Position.X, Y: e.Position.Y, Z: e.Position.Z,
		})
	})

	// Flash
	p.RegisterEventHandler(func(e events.FlashExplode) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		if e.Thrower != nil {
			throwerName = e.Thrower.Name
			throwerTeam = playerTeamString(e.Thrower.Team)
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: "flash", Action: "detonate", Tick: p.GameState().IngameTick(),
			EntityID: e.GrenadeEntityID, ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: e.Position.X, Y: e.Position.Y, Z: e.Position.Z,
		})
	})

	// Inferno (molotov/incendiary)
	p.RegisterEventHandler(func(e events.InfernoStart) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		grenType := "molotov"
		thrower := e.Inferno.Thrower()
		if thrower != nil {
			throwerName = thrower.Name
			throwerTeam = playerTeamString(thrower.Team)
			if thrower.Team == common.TeamCounterTerrorists {
				grenType = "incendiary"
			}
		}
		var x, y, z float64
		fires := e.Inferno.Fires().Active().List()
		if len(fires) > 0 {
			for _, f := range fires {
				x += f.X
				y += f.Y
				z += f.Z
			}
			x /= float64(len(fires))
			y /= float64(len(fires))
			z /= float64(len(fires))
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: grenType, Action: "start", Tick: p.GameState().IngameTick(),
			EntityID: int(e.Inferno.Entity.ID()), ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: x, Y: y, Z: z,
		})
	})

	p.RegisterEventHandler(func(e events.InfernoExpired) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		grenType := "molotov"
		thrower := e.Inferno.Thrower()
		if thrower != nil {
			throwerName = thrower.Name
			throwerTeam = playerTeamString(thrower.Team)
			if thrower.Team == common.TeamCounterTerrorists {
				grenType = "incendiary"
			}
		}
		var x, y, z float64
		fires := e.Inferno.Fires().List()
		if len(fires) > 0 {
			for _, f := range fires {
				x += f.X
				y += f.Y
				z += f.Z
			}
			x /= float64(len(fires))
			y /= float64(len(fires))
			z /= float64(len(fires))
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: grenType, Action: "expired", Tick: p.GameState().IngameTick(),
			EntityID: int(e.Inferno.Entity.ID()), ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: x, Y: y, Z: z,
		})
	})

	// Decoy
	p.RegisterEventHandler(func(e events.DecoyStart) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		if e.Thrower != nil {
			throwerName = e.Thrower.Name
			throwerTeam = playerTeamString(e.Thrower.Team)
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: "decoy", Action: "start", Tick: p.GameState().IngameTick(),
			EntityID: e.GrenadeEntityID, ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: e.Position.X, Y: e.Position.Y, Z: e.Position.Z,
		})
	})

	p.RegisterEventHandler(func(e events.DecoyExpired) {
		if srs.active { return }
		throwerName, throwerTeam := "", ""
		if e.Thrower != nil {
			throwerName = e.Thrower.Name
			throwerTeam = playerTeamString(e.Thrower.Team)
		}
		result.GrenadeEvents = append(result.GrenadeEvents, GrenadeEvent{
			Type: "decoy", Action: "expired", Tick: p.GameState().IngameTick(),
			EntityID: e.GrenadeEntityID, ThrowerName: throwerName, ThrowerTeam: throwerTeam,
			X: e.Position.X, Y: e.Position.Y, Z: e.Position.Z,
		})
	})

	// Bomb — plant
	p.RegisterEventHandler(func(e events.BombPlantBegin) {
		bs.isPlanting = true
		bs.plantBeginTick = p.GameState().IngameTick()
		if e.Player != nil {
			bs.plantingPlayer = e.Player.Name
			pos := e.Player.Position()
			bs.plantBeginX = pos.X
			bs.plantBeginY = pos.Y
		}
	})

	p.RegisterEventHandler(func(e events.BombPlanted) {
		gs := p.GameState()
		bs.plantTick = gs.IngameTick()
		bombPos := gs.Bomb().Position()
		bs.plantX = float64(bombPos.X)
		bs.plantY = float64(bombPos.Y)
		// Fallback: if bomb position is (0,0), use planting player's position
		if bs.plantX == 0 && bs.plantY == 0 && e.Player != nil {
			pos := e.Player.Position()
			bs.plantX = pos.X
			bs.plantY = pos.Y
		}
		bs.isPlanting = false
		bs.plantingPlayer = ""
		if bs.plantTick > 0 {
			bs.plantBeginTick = 0
		}
	})

	p.RegisterEventHandler(func(e events.BombPlantAborted) {
		bs.isPlanting = false
		bs.plantingPlayer = ""
		bs.plantBeginTick = 0
		bs.plantBeginX = 0
		bs.plantBeginY = 0
	})

	// Bomb — defuse
	p.RegisterEventHandler(func(e events.BombDefuseStart) {
		bs.isDefusing = true
		if e.Player != nil {
			bs.defusingPlayer = e.Player.Name
		}
	})

	p.RegisterEventHandler(func(e events.BombDefuseAborted) {
		bs.isDefusing = false
		bs.defusingPlayer = ""
	})

	p.RegisterEventHandler(func(e events.BombDefused) {
		bs.isDefusing = false
		bs.defusingPlayer = ""
		bs.plantTick = 0
		bs.plantX = 0
		bs.plantY = 0
	})

	p.RegisterEventHandler(func(e events.BombExplode) {
		bs.isDefusing = false
		bs.defusingPlayer = ""
		bs.plantTick = 0
		bs.plantX = 0
		bs.plantY = 0
	})

	// Round lifecycle
	p.RegisterEventHandler(func(e events.RoundStart) {
		bs.plantTick = 0
		bs.plantX = 0.0
		bs.plantY = 0.0
		bs.isPlanting = false
		bs.isDefusing = false
		bs.plantingPlayer = ""
		bs.defusingPlayer = ""
		bs.plantBeginTick = 0
		bs.plantBeginX = 0
		bs.plantBeginY = 0
		srs.inFreeze = true
		// Buffer the freeze tick — used for knife/phantom detection signals
		gs := p.GameState()
		ft := TickData{
			Tick:          gs.IngameTick(),
			Players:       []PlayerPosition{},
			IsFreezeStart: true,
			RoundNumber:   *roundNumber + 1,
		}
		srs.pendingFreeze = &ft
	})

	p.RegisterEventHandler(func(e events.RoundFreezetimeEnd) {
		gs := p.GameState()
		srs.inFreeze = false

		// Retroactive phantom purge: round started after knife but never got a RoundEnd
		if srs.phantomPossible && *roundNumber > 0 {
			srs.phantomPossible = false
			result.Ticks = result.Ticks[:srs.tickSnapshot]
			result.Shots = result.Shots[:srs.shotSnapshot]
			result.Damages = result.Damages[:srs.damageSnapshot]
			result.Kills = result.Kills[:srs.killSnapshot]
			result.GrenadeEvents = result.GrenadeEvents[:srs.grenadeSnapshot]
			*roundNumber = srs.roundNumberSnapshot
			if srs.pendingFreeze != nil {
				srs.pendingFreeze.RoundNumber = *roundNumber + 1
			}
		}

		// Knife round detection: all alive players have only knives
		if *roundNumber == 0 && isKnifeRound(gs) {
			srs.active = true
			srs.isKnifeSkip = true
			srs.pendingFreeze = nil
			return
		}

		// Clear buffered freeze tick (no longer emitted — freeze frames are skipped entirely)
		srs.pendingFreeze = nil

		*roundNumber++
		spawnTick := TickData{
			Tick:         gs.IngameTick(),
			Players:      []PlayerPosition{},
			IsRoundStart: true,
			RoundNumber:  *roundNumber,
			ScoreCT:      gs.TeamCounterTerrorists().Score(),
			ScoreT:       gs.TeamTerrorists().Score(),
		}
		for _, player := range gs.TeamCounterTerrorists().Members() {
			if player != nil && player.IsAlive() {
				spawnTick.Players = append(spawnTick.Players, playerToPosition(player, "CT"))
			}
		}
		for _, player := range gs.TeamTerrorists().Members() {
			if player != nil && player.IsAlive() {
				spawnTick.Players = append(spawnTick.Players, playerToPosition(player, "T"))
			}
		}
		if len(spawnTick.Players) > 0 {
			appendOrMergeTick(&result.Ticks, spawnTick)
		}
	})

	p.RegisterEventHandler(func(e events.RoundEnd) {
		if srs.active {
			if srs.isKnifeSkip {
				// After knife round: record snapshots for potential phantom purge
				srs.phantomPossible = true
				srs.tickSnapshot = len(result.Ticks)
				srs.shotSnapshot = len(result.Shots)
				srs.damageSnapshot = len(result.Damages)
				srs.killSnapshot = len(result.Kills)
				srs.grenadeSnapshot = len(result.GrenadeEvents)
				srs.roundNumberSnapshot = *roundNumber
			}
			srs.active = false
			srs.isKnifeSkip = false
			return
		}

		gs := p.GameState()
		winner := ""
		if e.Winner == common.TeamCounterTerrorists {
			winner = "CT"
		} else if e.Winner == common.TeamTerrorists {
			winner = "T"
		}

		// Retroactive phantom detection: round after knife with no winner = phantom
		if srs.phantomPossible {
			srs.phantomPossible = false
			if winner == "" {
				// Phantom confirmed: purge all data emitted during this round
				result.Ticks = result.Ticks[:srs.tickSnapshot]
				result.Shots = result.Shots[:srs.shotSnapshot]
				result.Damages = result.Damages[:srs.damageSnapshot]
				result.Kills = result.Kills[:srs.killSnapshot]
				result.GrenadeEvents = result.GrenadeEvents[:srs.grenadeSnapshot]
				*roundNumber = srs.roundNumberSnapshot
				return
			}
			// winner CT/T = real round, keep data and continue normally
		}

		endTick := TickData{
			Tick:               gs.IngameTick(),
			Players:            []PlayerPosition{},
			IsRoundEnd:         true,
			RoundNumber:        *roundNumber,
			Winner:             winner,
			ScoreCT:            gs.TeamCounterTerrorists().Score(),
			ScoreT:             gs.TeamTerrorists().Score(),
			BombPlantTick:      bs.plantTick,
			BombPlantX:         bs.plantX,
			BombPlantY:         bs.plantY,
			IsPlanting:         bs.isPlanting,
			IsDefusing:         bs.isDefusing,
			PlantingPlayerName: bs.plantingPlayer,
			DefusingPlayerName: bs.defusingPlayer,
		}
		for _, player := range gs.TeamCounterTerrorists().Members() {
			if player != nil {
				endTick.Players = append(endTick.Players, playerToPosition(player, "CT"))
			}
		}
		for _, player := range gs.TeamTerrorists().Members() {
			if player != nil {
				endTick.Players = append(endTick.Players, playerToPosition(player, "T"))
			}
		}
		appendOrMergeTick(&result.Ticks, endTick)
	})
}
