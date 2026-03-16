package main

import (
	"time"

	common "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/common"
	events "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/events"
)

func hitGroupString(hg events.HitGroup) string {
	switch hg {
	case events.HitGroupHead:
		return "head"
	case events.HitGroupChest:
		return "chest"
	case events.HitGroupStomach:
		return "stomach"
	case events.HitGroupLeftArm, events.HitGroupRightArm:
		return "arm"
	case events.HitGroupLeftLeg, events.HitGroupRightLeg:
		return "leg"
	default:
		return "body"
	}
}

func playerTeamString(team common.Team) string {
	switch team {
	case common.TeamCounterTerrorists:
		return "CT"
	case common.TeamTerrorists:
		return "T"
	default:
		return ""
	}
}

func grenadeTypeString(eqType common.EquipmentType) string {
	switch eqType {
	case common.EqSmoke:
		return "smoke"
	case common.EqFlash:
		return "flash"
	case common.EqHE:
		return "he"
	case common.EqMolotov:
		return "molotov"
	case common.EqIncendiary:
		return "incendiary"
	case common.EqDecoy:
		return "decoy"
	default:
		return "unknown"
	}
}

func playerToPosition(player *common.Player, team string) PlayerPosition {
	pos := player.Position()
	weapon := ""
	if w := player.ActiveWeapon(); w != nil {
		weapon = w.String()
	}

	var equipment []string
	for _, w := range player.Weapons() {
		if w != nil {
			equipment = append(equipment, w.String())
		}
	}

	isScoped := false
	if pawn := player.PlayerPawnEntity(); pawn != nil {
		if val, ok := pawn.PropertyValue("m_bIsScoped"); ok {
			isScoped = val.BoolVal()
		}
	}

	return PlayerPosition{
		Name:         player.Name,
		Team:         team,
		X:            pos.X,
		Y:            pos.Y,
		Z:            pos.Z,
		Yaw:          float64(player.ViewDirectionX()),
		Health:       player.Health(),
		IsAlive:      player.IsAlive(),
		ActiveWeapon: weapon,
		Money:        player.Money(),
		Armor:        player.Armor(),
		HasHelmet:     player.HasHelmet(),
		HasDefuseKit:  player.HasDefuseKit(),
		Equipment:     equipment,
		FlashDuration: float64(player.FlashDurationTimeRemaining()) / float64(time.Second),
		IsScoped:      isScoped,
	}
}
