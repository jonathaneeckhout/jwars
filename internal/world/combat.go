package world

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type combatTickResult struct {
	destroyed          []Unit
	destroyedBuildings []Building
}

func loadPlayerIDs(ctx context.Context, tx pgx.Tx) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT player_id FROM season_players
		WHERE season_number=(SELECT season_number FROM world_season WHERE singleton=TRUE)
		ORDER BY player_id`)
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

func (w *World) advanceCombatTick(ctx context.Context, tx pgx.Tx, tick int64, beforeUnits []Unit, buildings []Building, deposits []ResourceDeposit) (combatTickResult, error) {
	result := combatTickResult{destroyed: make([]Unit, 0), destroyedBuildings: make([]Building, 0)}
	oldByID := make(map[string]Unit, len(beforeUnits))
	currentByID := make(map[string]Unit, len(beforeUnits))
	buildingsByID := make(map[string]Building, len(buildings))
	for _, unit := range beforeUnits {
		oldByID[unit.ID] = unit
		currentByID[unit.ID] = unit
	}
	for _, building := range buildings {
		buildingsByID[building.ID] = building
	}
	oldVisible := make(map[string]map[entityKey]bool)
	depositByID := make(map[string]ResourceDeposit, len(deposits))
	for _, deposit := range deposits {
		depositByID[deposit.ID] = deposit
	}
	for _, unit := range beforeUnits {
		if _, exists := oldVisible[unit.OwnerID]; !exists {
			oldVisible[unit.OwnerID] = w.visibleEntities(unit.OwnerID, beforeUnits, buildings)
		}
	}

	// Every order reads the same pre-movement state, so database row order cannot
	// change which target an attacking unit pursues this tick.
	for _, old := range beforeUnits {
		unit := currentByID[old.ID]
		if unit.AttackTargetID != nil {
			target, exists := oldByID[*unit.AttackTargetID]
			if !exists || w.unitAttackDamage(unit.Kind) == 0 {
				unit.AttackTargetID, unit.AttackTargetX, unit.AttackTargetY = nil, nil, nil
			} else {
				if oldVisible[unit.OwnerID][entityKey("unit:"+target.ID)] {
					x, y := target.X, target.Y
					unit.AttackTargetX, unit.AttackTargetY = &x, &y
				}
				if unit.AttackTargetX != nil && unit.AttackTargetY != nil {
					targetX, targetY := *unit.AttackTargetX, *unit.AttackTargetY
					visible := oldVisible[unit.OwnerID][entityKey("unit:"+target.ID)]
					if !visible || chebyshev(unit.X, unit.Y, targetX, targetY) > w.unitAttackRange(unit.Kind) {
						unit.X, unit.Y = stepToward(unit.X, unit.Y, targetX, targetY)
					}
				}
			}
		} else if unit.AttackTargetBuildingID != nil {
			building, exists := buildingsByID[*unit.AttackTargetBuildingID]
			if !exists || !w.buildingDefinition(building.Kind).Destructible || w.unitAttackDamage(unit.Kind) == 0 {
				unit.AttackTargetBuildingID, unit.AttackTargetX, unit.AttackTargetY = nil, nil, nil
			} else {
				if oldVisible[unit.OwnerID][entityKey("building:"+building.ID)] {
					x, y := building.X, building.Y
					unit.AttackTargetX, unit.AttackTargetY = &x, &y
				}
				if unit.AttackTargetX != nil && unit.AttackTargetY != nil && distanceToBuilding(unit.X, unit.Y, building.X, building.Y, building.Width, building.Height) > w.unitAttackRange(unit.Kind) {
					unit.X, unit.Y = stepToward(unit.X, unit.Y, *unit.AttackTargetX, *unit.AttackTargetY)
				}
			}
		} else if unit.GatherTargetID != nil {
			deposit, exists := depositByID[*unit.GatherTargetID]
			if !exists {
				unit.GatherTargetID = nil
				unit.GatherProgress = 0
			} else if chebyshev(unit.X, unit.Y, deposit.X, deposit.Y) > w.config.Economy.GatherRange {
				unit.X, unit.Y = stepToward(unit.X, unit.Y, deposit.X, deposit.Y)
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
			visibleAfterMove[unit.OwnerID] = w.visibleEntities(unit.OwnerID, postMove, buildings)
		}
	}
	damage := make(map[string]int)
	for _, building := range buildings {
		definition := w.buildingDefinition(building.Kind)
		if building.Status == "complete" && definition.AttackDamage > 0 && tick%definition.AttackIntervalTicks == 0 {
			for _, unit := range postMove {
				if unit.OwnerID != building.OwnerID && chebyshevToBuilding(unit.X, unit.Y, building) <= definition.AttackRange {
					damage[unit.ID] += definition.AttackDamage
				}
			}
		}
	}
	buildingDamage := make(map[string]int)
	for _, unit := range postMove {
		if w.unitAttackDamage(unit.Kind) == 0 {
			continue
		}
		if unit.AttackTargetID != nil {
			target, exists := currentByID[*unit.AttackTargetID]
			if exists && visibleAfterMove[unit.OwnerID][entityKey("unit:"+target.ID)] && chebyshev(unit.X, unit.Y, target.X, target.Y) <= w.unitAttackRange(unit.Kind) {
				damage[target.ID] += w.unitAttackDamage(unit.Kind)
			}
		}
		if unit.AttackTargetBuildingID != nil {
			target, exists := buildingsByID[*unit.AttackTargetBuildingID]
			if exists && target.OwnerID != unit.OwnerID && w.buildingDefinition(target.Kind).Destructible && visibleAfterMove[unit.OwnerID][entityKey("building:"+target.ID)] && distanceToBuilding(unit.X, unit.Y, target.X, target.Y, target.Width, target.Height) <= w.unitAttackRange(unit.Kind) {
				buildingDamage[target.ID] += w.unitAttackDamage(unit.Kind)
			}
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
	destroyedBuildingIDs := make(map[string]bool)
	for id, amount := range buildingDamage {
		building, exists := buildingsByID[id]
		if !exists {
			continue
		}
		building.Health -= amount
		if building.Health <= 0 {
			building.Health = 0
			destroyedBuildingIDs[id] = true
			result.destroyedBuildings = append(result.destroyedBuildings, building)
		} else {
			buildingsByID[id] = building
			if _, err := tx.Exec(ctx, `UPDATE buildings SET health=$2 WHERE id=$1`, id, building.Health); err != nil {
				return result, err
			}
		}
	}
	for _, building := range result.destroyedBuildings {
		rows, err := tx.Query(ctx, `SELECT id,building_id,unit_kind,started_tick,completion_tick FROM training_orders WHERE building_id=$1`, building.ID)
		if err != nil {
			return result, err
		}
		cancelled := make([]TrainingOrder, 0)
		for rows.Next() {
			var order TrainingOrder
			if err := rows.Scan(&order.ID, &order.BuildingID, &order.UnitKind, &order.StartedTick, &order.CompletionTick); err != nil {
				rows.Close()
				return result, err
			}
			cancelled = append(cancelled, order)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return result, err
		}
		for _, order := range cancelled {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: building.OwnerID, Tick: tick, Type: "training.cancelled", Training: []TrainingOrder{order}}); err != nil {
				return result, err
			}
		}
		if _, err := tx.Exec(ctx, `DELETE FROM buildings WHERE id=$1`, building.ID); err != nil {
			return result, err
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
		if unit.AttackTargetBuildingID != nil && destroyedBuildingIDs[*unit.AttackTargetBuildingID] {
			unit.AttackTargetBuildingID, unit.AttackTargetX, unit.AttackTargetY = nil, nil, nil
			currentByID[id] = unit
		}
	}

	for id, unit := range currentByID {
		if dead[id] {
			continue
		}
		if _, err := tx.Exec(ctx, `
			UPDATE units SET x=$2, y=$3, health=$4, target_x=$5, target_y=$6,
				target_unit_id=$7, target_building_id=$8, attack_target_x=$9, attack_target_y=$10, gather_target_id=$11, gather_progress=$12
			WHERE id=$1`, id, unit.X, unit.Y, unit.Health, unit.TargetX, unit.TargetY,
			unit.AttackTargetID, unit.AttackTargetBuildingID, unit.AttackTargetX, unit.AttackTargetY, unit.GatherTargetID, unit.GatherProgress); err != nil {
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

func (w *World) emitWorldEvents(ctx context.Context, tx pgx.Tx, tick int64, players []string, beforeUnits []Unit, beforeBuildings []Building, afterUnits []Unit, afterBuildings []Building, beforeDeposits []ResourceDeposit, afterDeposits []ResourceDeposit, destroyed []Unit, destroyedBuildings []Building) error {
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
	oldDeposits := make(map[string]ResourceDeposit, len(beforeDeposits))
	newDeposits := make(map[string]ResourceDeposit, len(afterDeposits))
	for _, deposit := range beforeDeposits {
		oldDeposits[deposit.ID] = deposit
	}
	for _, deposit := range afterDeposits {
		newDeposits[deposit.ID] = deposit
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
	deadBuildings := make(map[string]Building, len(destroyedBuildings))
	for _, building := range destroyedBuildings {
		deadBuildings[building.ID] = building
	}

	for _, playerID := range players {
		wasVisible := w.visibleEntities(playerID, beforeUnits, beforeBuildings)
		isVisible := w.visibleEntities(playerID, afterUnits, afterBuildings)
		spottedDeposits := make([]ResourceDeposit, 0)
		changedDeposits := make([]ResourceDeposit, 0)
		for id, deposit := range newDeposits {
			wasSeen := w.depositVisible(playerID, oldDeposits[id], beforeUnits, beforeBuildings)
			isSeen := w.depositVisible(playerID, deposit, afterUnits, afterBuildings)
			if !isSeen {
				continue
			}
			if !wasSeen {
				spottedDeposits = append(spottedDeposits, deposit)
			} else if oldDeposits[id].Amount != deposit.Amount {
				changedDeposits = append(changedDeposits, deposit)
			}
		}
		spottedUnits := make([]Unit, 0)
		spottedBuildings := make([]Building, 0)
		hidden := make([]EntityRef, 0)
		changedUnits := make([]Unit, 0)
		damagedUnits := make([]Unit, 0)
		destroyedUnits := make([]Unit, 0)
		changedBuildings := make([]Building, 0)
		damagedBuildings := make([]Building, 0)
		destroyedBuildingUpdates := make([]Building, 0)

		for id, unit := range newUnits {
			key := entityKey("unit:" + id)
			if !isVisible[key] {
				continue
			}
			old, existed := oldUnits[id]
			if !existed || !wasVisible[key] {
				if unit.OwnerID != playerID {
					spottedUnits = append(spottedUnits, w.enrichUnit(unit, playerID))
				}
				continue
			}
			if unit.Health != old.Health {
				damagedUnits = append(damagedUnits, w.enrichUnit(unit, playerID))
			}
			if unit.X != old.X || unit.Y != old.Y || !sameIntPointer(unit.TargetX, old.TargetX) || !sameIntPointer(unit.TargetY, old.TargetY) || !sameStringPointer(unit.AttackTargetID, old.AttackTargetID) || !sameStringPointer(unit.AttackTargetBuildingID, old.AttackTargetBuildingID) {
				changedUnits = append(changedUnits, w.enrichUnit(unit, playerID))
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
					spottedBuildings = append(spottedBuildings, w.enrichBuilding(building))
				}
				continue
			}
			if building.Health != old.Health {
				damagedBuildings = append(damagedBuildings, w.enrichBuilding(building))
			}
			if building.OwnerID != playerID && (building.Status != old.Status || constructionMilestone(old, tick-1) != constructionMilestone(building, tick)) {
				changedBuildings = append(changedBuildings, w.enrichBuilding(building))
			}
		}
		for key := range wasVisible {
			if isVisible[key] {
				continue
			}
			ref := entityReference(key)
			if ref.Type == "building" && deadBuildings[ref.ID].ID != "" {
				continue
			}
			if ref.Type == "unit" && dead[ref.ID].ID != "" {
				continue
			}
			if (ref.Type == "unit" && newUnits[ref.ID].ID != "") || (ref.Type == "building" && newBuildings[ref.ID].ID != "") {
				hidden = append(hidden, ref)
			}
		}
		for id, deposit := range oldDeposits {
			if w.depositVisible(playerID, deposit, beforeUnits, beforeBuildings) && !w.depositVisible(playerID, newDeposits[id], afterUnits, afterBuildings) {
				hidden = append(hidden, EntityRef{ID: id, Type: "resource_deposit"})
			}
		}
		for id, unit := range dead {
			if unit.OwnerID == playerID || wasVisible[entityKey("unit:"+id)] {
				destroyedUnits = append(destroyedUnits, w.enrichUnit(unit, playerID))
			}
		}
		for id, building := range deadBuildings {
			if building.OwnerID == playerID || wasVisible[entityKey("building:"+id)] {
				destroyedBuildingUpdates = append(destroyedBuildingUpdates, w.enrichBuilding(building))
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
		if len(damagedBuildings) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "buildings.damaged", Buildings: damagedBuildings}); err != nil {
				return err
			}
		}
		if len(destroyedBuildingUpdates) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "buildings.destroyed", Buildings: destroyedBuildingUpdates}); err != nil {
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
		if len(spottedDeposits) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "resource_deposits.spotted", Deposits: spottedDeposits}); err != nil {
				return err
			}
		}
		if len(changedDeposits) > 0 {
			if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "resource_deposits.changed", Deposits: changedDeposits}); err != nil {
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
