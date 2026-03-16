package main

import (
	common "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs/common"
	dem "github.com/markus-wa/demoinfocs-golang/v4/pkg/demoinfocs"
)

// collectDroppedItems returns all weapons/grenades lying on the ground (Owner == nil).
func collectDroppedItems(gs dem.GameState) []DroppedItem {
	var items []DroppedItem
	for _, wep := range gs.Weapons() {
		if wep == nil || wep.Owner != nil {
			continue
		}
		// Skip knives and C4 (tracked separately via bombState)
		if wep.Type == common.EqKnife || wep.Type == common.EqBomb {
			continue
		}
		if wep.Entity == nil {
			continue
		}
		pos := wep.Entity.Position()
		if pos.X == 0 && pos.Y == 0 {
			continue // invalid entity
		}
		items = append(items, DroppedItem{
			EntityID: int(wep.Entity.ID()),
			Weapon:   wep.String(),
			X:        pos.X,
			Y:        pos.Y,
			Z:        pos.Z,
		})
	}
	return items
}
