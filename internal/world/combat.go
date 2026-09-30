package world

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type combatTickResult struct {
	destroyed []Unit
}

func loadPlayerIDs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `SELECT player_id FROM players ORDER BY player_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	players := make([]string, 0)
	for rows.Next() {
		var playerID string
		if err := rows.Scan(&playerID); err != nil {
			return nil, err
		}
		players = append(players, playerID)
	}
	return players, rows.Err()
}

func advanceCombatTick(ctx context.Context, tx pgx.Tx, beforeUnits []Unit, buildings []Building) (combatTickResult, error) {
	result := combatTickResult{destroyed: make([]Unit, 0)}
	oldByID := make(map[string]Unit, len(beforeUnits))
	currentByID := make(map[string]Unit, len(beforeUnits))
	for _, unit := range beforeUnits {
		oldByID[unit.ID] = unit
		currentByID[unit.ID] = unit
	}
	oldVisible := make(map[string]map[entityKey]bool)
	for _, unit := range beforeUnits {
		if _, exists := oldVisible[unit.OwnerID]; !exists {
			oldVisible[unit.OwnerID] = visibleEntities(unit.OwnerID, beforeUnits, buildings)
		}
	}

	// Every order reads the same pre-movement state, so database row order cannot
	// change which target an attacking unit pursues this tick.
	for _, old := range beforeUnits {
		unit := currentByID[old.ID]
		if unit.AttackTargetID != nil {
			target, exists := oldByID[*unit.AttackTargetID]
			if !exists || unitAttackDamage(unit.Kind) == 0 {
				unit.AttackTargetID, unit.AttackTargetX, unit.AttackTargetY = nil, nil, nil
			} else {
				if oldVisible[unit.OwnerID][entityKey("unit:"+target.ID)] {
					x, y := target.X, target.Y
					unit.AttackTargetX, unit.AttackTargetY = &x, &y
				}
				if unit.AttackTargetX != nil && unit.AttackTargetY != nil {
					targetX, targetY := *unit.AttackTargetX, *unit.AttackTargetY
					visible := oldVisible[unit.OwnerID][entityKey("unit:"+target.ID)]
					if !visible || chebyshev(unit.X, unit.Y, targetX, targetY) > unitAttackRange(unit.Kind) {
						unit.X, unit.Y = stepToward(unit.X, unit.Y, targetX, targetY)
					}
				}
			}
		} else if unit.TargetX != nil && unit.TargetY != nil {
			unit.X, unit.Y = stepToward(unit.X, unit.Y, *unit.TargetX, *unit.TargetY)
			if unit.X == *unit.TargetX && unit.Y == *unit.TargetY {
				unit.TargetX, unit.TargetY = nil, nil
			}
		}
		currentByID[unit.ID] = unit
	}

	postMove := valuesOfUnits(currentByID)
	visibleAfterMove := make(map[string]map[entityKey]bool)
	for _, unit := range postMove {
		if _, exists := visibleAfterMove[unit.OwnerID]; !exists {
			visibleAfterMove[unit.OwnerID] = visibleEntities(unit.OwnerID, postMove, buildings)
		}
	}
	damage := make(map[string]int)
	for _, unit := range postMove {
		if unit.AttackTargetID == nil || unitAttackDamage(unit.Kind) == 0 {
			continue
		}
		target, exists := currentByID[*unit.AttackTargetID]
		if exists && visibleAfterMove[unit.OwnerID][entityKey("unit:"+target.ID)] && chebyshev(unit.X, unit.Y, target.X, target.Y) <= unitAttackRange(unit.Kind) {
			damage[target.ID] += unitAttackDamage(unit.Kind)
		}
	}

	dead := make(map[string]bool)
	for id, amount := range damage {
		unit := currentByID[id]
		unit.Health -= amount
		if unit.Health <= 0 {
			unit.Health = 0
			dead[id] = true
			result.destroyed = append(result.destroyed, unit)
		} else {
			currentByID[id] = unit
		}
	}
	for id, unit := range currentByID {
		if dead[id] {
			continue
		}
		if unit.AttackTargetID != nil && dead[*unit.AttackTargetID] {
			unit.AttackTargetID, unit.AttackTargetX, unit.AttackTargetY = nil, nil, nil
			currentByID[id] = unit
		}
	}

	for id, unit := range currentByID {
		if dead[id] {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE units SET x=$2, y=$3, health=$4, target_x=$5, target_y=$6,
				target_unit_id=$7, attack_target_x=$8, attack_target_y=$9
			WHERE id=$1`, id, unit.X, unit.Y, unit.Health, unit.TargetX, unit.TargetY,
			unit.AttackTargetID, unit.AttackTargetX, unit.AttackTargetY); err != nil {
			return result, err
		}
	}
	for id := range dead {
		if _, err := tx.Exec(ctx, `DELETE FROM units WHERE id=$1`, id); err != nil {
			return result, err
		}
	}
	return result, nil
}

func valuesOfUnits(units map[string]Unit) []Unit {
	result := make([]Unit, 0, len(units))
	for _, unit := range units {
		result = append(result, unit)
	}
	return result
}

func emitWorldEvents(ctx context.Context, tx pgx.Tx, tick int64, players []string, beforeUnits []Unit, beforeBuildings []Building, afterUnits []Unit, afterBuildings []Building, destroyed []Unit) error {
	oldUnits := make(map[string]Unit, len(beforeUnits))
	newUnits := make(map[string]Unit, len(afterUnits))
	oldBuildings := make(map[string]Building, len(beforeBuildings))
	newBuildings := make(map[string]Building, len(afterBuildings))
	for _, unit := range beforeUnits {
		oldUnits[unit.ID] = unit
	}
	for _, unit := range afterUnits {
		newUnits[unit.ID] = unit
	}
	for _, building := range beforeBuildings {
		oldBuildings[building.ID] = building
	}
	for _, building := range afterBuildings {
		newBuildings[building.ID] = building
	}
	dead := make(map[string]Unit, len(destroyed))
	for _, unit := range destroyed {
		dead[unit.ID] = unit
	}

	for _, playerID := range players {
		wasVisible := visibleEntities(playerID, beforeUnits, beforeBuildings)
		isVisible := visibleEntities(playerID, afterUnits, afterBuildings)
		spottedUnits := make([]Unit, 0)
		spottedBuildings := make([]Building, 0)
		hidden := make([]EntityRef, 0)
		changedUnits := make([]Unit, 0)
		damagedUnits := make([]Unit, 0)
		destroyedUnits := make([]Unit, 0)
		changedBuildings := make([]Building, 0)

		for id, unit := range newUnits {
			key := entityKey("unit:" + id)
			if !isVisible[key] {
				continue
			}
			old, existed := oldUnits[id]
			if !existed || !wasVisible[key] {
				if unit.OwnerID != playerID {
					spottedUnits = append(spottedUnits, enrichUnit(unit, playerID))
				}
				continue
			}
			if unit.Health != old.Health {
				damagedUnits = append(damagedUnits, enrichUnit(unit, playerID))
			}
			if unit.X != old.X || unit.Y != old.Y || !sameIntPointer(unit.TargetX, old.TargetX) || !sameIntPointer(unit.TargetY, old.TargetY) || !sameStringPointer(unit.AttackTargetID, old.AttackTargetID) {
				changedUnits = append(changedUnits, enrichUnit(unit, playerID))
			}
		}
		for id, building := range newBuildings {
			key := entityKey("building:" + id)
			if !isVisible[key] {
				continue
			}
			old, existed := oldBuildings[id]
			if !existed || !wasVisible[key] {
				if building.OwnerID != playerID {
					spottedBuildings = append(spottedBuildings, enrichBuilding(building))
				}
				continue
			}
			if building.OwnerID != playerID && (building.Status != old.Status || constructionMilestone(old, tick-1) != constructionMilestone(building, tick)) {
				changedBuildings = append(changedBuildings, enrichBuilding(building))
			}
		}
		for key := range wasVisible {
			if isVisible[key] {
				continue
			}
			ref := entityReference(key)
			if ref.Type == "unit" && dead[ref.ID].ID != "" {
				continue
			}
			if (ref.Type == "unit" && newUnits[ref.ID].ID != "") || (ref.Type == "building" && newBuildings[ref.ID].ID != "") {
				hidden = append(hidden, ref)
			}
		}
		for id, unit := range dead {
			if unit.OwnerID == playerID || wasVisible[entityKey("unit:"+id)] {
				destroyedUnits = append(destroyedUnits, enrichUnit(unit, playerID))
			}
		}
		if len(destroyedUnits) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "units.destroyed", Units: destroyedUnits}); err != nil {
				return err
			}
		}
		if len(damagedUnits) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "units.damaged", Units: damagedUnits}); err != nil {
				return err
			}
		}
		if len(changedUnits) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "units.changed", Units: changedUnits}); err != nil {
				return err
			}
		}
		if len(changedBuildings) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "buildings.changed", Buildings: changedBuildings}); err != nil {
				return err
			}
		}
		if len(spottedUnits) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "units.spotted", Units: spottedUnits}); err != nil {
				return err
			}
		}
		if len(spottedBuildings) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "buildings.spotted", Buildings: spottedBuildings}); err != nil {
				return err
			}
		}
		if len(hidden) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "entities.hidden", Entities: hidden}); err != nil {
				return err
			}
		}
	}
	return nil
}

func appendWorldEvent(ctx context.Context, tx pgx.Tx, update Update) error {
	_, err := appendEvent(ctx, tx, update)
	return err
}

func sameIntPointer(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameStringPointer(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
