package world

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

func (w *World) seedResourceDeposits(ctx context.Context) error {
	var count int
	if err := w.pool.QueryRow(ctx, `SELECT count(*) FROM resource_deposits`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, point := range w.config.Economy.DepositLocations {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO resource_deposits (id,kind,x,y,amount,capacity) VALUES ($1,'materials',$2,$3,$4,$4)`, id, point.X, point.Y, w.config.Economy.DepositCapacity); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func loadResourceDeposits(ctx context.Context, tx pgx.Tx) ([]ResourceDeposit, error) {
	rows, err := tx.Query(ctx, `SELECT id,kind,x,y,amount,capacity FROM resource_deposits ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	deposits := make([]ResourceDeposit, 0)
	for rows.Next() {
		var deposit ResourceDeposit
		if err := rows.Scan(&deposit.ID, &deposit.Kind, &deposit.X, &deposit.Y, &deposit.Amount, &deposit.Capacity); err != nil {
			return nil, err
		}
		deposits = append(deposits, deposit)
	}
	return deposits, rows.Err()
}

func (w *World) loadVisibleDeposits(ctx context.Context, tx pgx.Tx, playerID string) ([]ResourceDeposit, error) {
	units, err := loadUnits(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	buildings, err := loadBuildings(ctx, tx, false)
	if err != nil {
		return nil, err
	}
	deposits, err := loadResourceDeposits(ctx, tx)
	if err != nil {
		return nil, err
	}
	visible := make([]ResourceDeposit, 0)
	for _, deposit := range deposits {
		if w.depositVisible(playerID, deposit, units, buildings) {
			visible = append(visible, deposit)
		}
	}
	return visible, nil
}

func (w *World) depositVisible(playerID string, deposit ResourceDeposit, units []Unit, buildings []Building) bool {
	for _, unit := range units {
		if unit.OwnerID == playerID && chebyshev(unit.X, unit.Y, deposit.X, deposit.Y) <= w.unitVisionRange(unit.Kind) {
			return true
		}
	}
	for _, building := range buildings {
		if building.OwnerID == playerID && w.buildingVisionRange(building) > 0 && chebyshevToBuilding(deposit.X, deposit.Y, building) <= w.buildingVisionRange(building) {
			return true
		}
	}
	return false
}

func (w *World) regenerateDeposits(ctx context.Context, tx pgx.Tx, tick int64) error {
	if tick%w.config.Economy.DepositRegenerationIntervalTicks != 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `UPDATE resource_deposits SET amount=LEAST(capacity,amount+$1) WHERE amount<capacity`, w.config.Economy.DepositRegenerationAmount)
	return err
}

func loadTrainingOrders(ctx context.Context, tx pgx.Tx, playerID string, tick int64) ([]TrainingOrder, error) {
	rows, err := tx.Query(ctx, `SELECT id,building_id,unit_kind,started_tick,completion_tick FROM training_orders WHERE player_id=$1 ORDER BY started_tick,id`, playerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	orders := make([]TrainingOrder, 0)
	for rows.Next() {
		var order TrainingOrder
		if err := rows.Scan(&order.ID, &order.BuildingID, &order.UnitKind, &order.StartedTick, &order.CompletionTick); err != nil {
			return nil, err
		}
		duration := order.CompletionTick - order.StartedTick
		order.ProgressPct = int((tick - order.StartedTick) * 100 / duration)
		if order.ProgressPct > 100 {
			order.ProgressPct = 100
		}
		orders = append(orders, order)
	}
	return orders, rows.Err()
}

func (w *World) completeTrainingOrders(ctx context.Context, tx pgx.Tx, tick int64) error {
	rows, err := tx.Query(ctx, `SELECT id,player_id,building_id,unit_kind,started_tick,completion_tick FROM training_orders WHERE completion_tick <= $1 ORDER BY building_id FOR UPDATE`, tick)
	if err != nil {
		return err
	}
	type readyOrder struct {
		order    TrainingOrder
		playerID string
	}
	ready := make([]readyOrder, 0)
	for rows.Next() {
		var item readyOrder
		if err := rows.Scan(&item.order.ID, &item.playerID, &item.order.BuildingID, &item.order.UnitKind, &item.order.StartedTick, &item.order.CompletionTick); err != nil {
			rows.Close()
			return err
		}
		ready = append(ready, item)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, item := range ready {
		item.order.ProgressPct = 100
		var building Building
		if err := tx.QueryRow(ctx, `SELECT id,player_id,kind,x,y,width,height,status,started_tick,build_ticks FROM buildings WHERE id=$1`, item.order.BuildingID).Scan(&building.ID, &building.OwnerID, &building.Kind, &building.X, &building.Y, &building.Width, &building.Height, &building.Status, &building.StartedTick, &building.BuildTicks); err != nil {
			return err
		}
		x, y, found, err := w.trainingSpawn(ctx, tx, building)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		unitID, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO units (id,player_id,kind,x,y,health) VALUES ($1,$2,$3,$4,$5,$6)`, unitID, item.playerID, item.order.UnitKind, x, y, w.unitMaxHealth(item.order.UnitKind)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM training_orders WHERE id=$1`, item.order.ID); err != nil {
			return err
		}
		unit := w.enrichUnit(Unit{ID: unitID, OwnerID: item.playerID, Kind: item.order.UnitKind, X: x, Y: y, Health: w.unitMaxHealth(item.order.UnitKind)}, item.playerID)
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: item.playerID, Tick: tick, Type: "training.completed", Units: []Unit{unit}, Training: []TrainingOrder{item.order}}); err != nil {
			return err
		}
	}
	return nil
}

// ensureWorkerRecovery prevents a player with no workers and insufficient
// materials from becoming permanently unable to play. The base starts one
// free, deliberately slow worker training order; normal worker training still
// costs materials and completes much faster.
func (w *World) ensureWorkerRecovery(ctx context.Context, tx pgx.Tx, tick int64) error {
	players, err := loadPlayerIDs(ctx, tx)
	if err != nil {
		return err
	}
	workerDefinition := w.unitDefinitions["worker"]
	for _, playerID := range players {
		var workers int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM units WHERE player_id=$1 AND kind='worker'`, playerID).Scan(&workers); err != nil {
			return err
		}
		if workers > 0 {
			continue
		}
		var alreadyTraining bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM training_orders WHERE player_id=$1 AND unit_kind='worker')`, playerID).Scan(&alreadyTraining); err != nil {
			return err
		}
		if alreadyTraining {
			continue
		}
		var materials int64
		if err := tx.QueryRow(ctx, `SELECT materials FROM player_resources WHERE player_id=$1`, playerID).Scan(&materials); err != nil {
			return err
		}
		if materials >= workerDefinition.TrainingCost {
			continue
		}
		var baseID string
		if err := tx.QueryRow(ctx, `SELECT id FROM buildings WHERE player_id=$1 AND kind='base' AND status='complete' ORDER BY id LIMIT 1`, playerID).Scan(&baseID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				continue
			}
			return err
		}
		orderID, err := newID()
		if err != nil {
			return err
		}
		order := TrainingOrder{ID: orderID, BuildingID: baseID, UnitKind: "worker", StartedTick: tick, CompletionTick: tick + workerDefinition.TrainingTicks*5}
		if _, err := tx.Exec(ctx, `INSERT INTO training_orders (id,player_id,building_id,unit_kind,started_tick,completion_tick) VALUES ($1,$2,$3,$4,$5,$6)`, order.ID, playerID, order.BuildingID, order.UnitKind, order.StartedTick, order.CompletionTick); err != nil {
			return err
		}
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "training.started", Training: []TrainingOrder{order}}); err != nil {
			return err
		}
	}
	return nil
}

func (w *World) trainingSpawn(ctx context.Context, tx pgx.Tx, barracks Building) (int, int, bool, error) {
	units, err := loadUnits(ctx, tx, false)
	if err != nil {
		return 0, 0, false, err
	}
	buildings, err := loadBuildings(ctx, tx, false)
	if err != nil {
		return 0, 0, false, err
	}
	candidates := make([]Point, 0)
	for offset := 0; offset < 16; offset++ {
		candidates = append(candidates,
			Point{X: barracks.X + offset, Y: barracks.Y - 1},
			Point{X: barracks.X + offset, Y: barracks.Y + barracks.Height},
			Point{X: barracks.X - 1, Y: barracks.Y + offset},
			Point{X: barracks.X + barracks.Width, Y: barracks.Y + offset})
	}
	for _, point := range candidates {
		if !w.insideMap(point.X, point.Y) {
			continue
		}
		occupied := false
		for _, unit := range units {
			if unit.X == point.X && unit.Y == point.Y {
				occupied = true
				break
			}
		}
		if occupied {
			continue
		}
		for _, building := range buildings {
			if point.X >= building.X && point.X < building.X+building.Width && point.Y >= building.Y && point.Y < building.Y+building.Height {
				occupied = true
				break
			}
		}
		if !occupied {
			return point.X, point.Y, true, nil
		}
	}
	return 0, 0, false, nil
}

func (w *World) advanceGathering(ctx context.Context, tx pgx.Tx, tick int64, units []Unit, deposits []ResourceDeposit) error {
	depositByID := make(map[string]ResourceDeposit, len(deposits))
	for _, deposit := range deposits {
		depositByID[deposit.ID] = deposit
	}
	amounts := make(map[string]int)
	for i := range units {
		unit := &units[i]
		if unit.Kind != "worker" || unit.GatherTargetID == nil {
			continue
		}
		deposit, exists := depositByID[*unit.GatherTargetID]
		if !exists {
			unit.GatherTargetID = nil
			unit.GatherProgress = 0
		} else if chebyshev(unit.X, unit.Y, deposit.X, deposit.Y) <= w.config.Economy.GatherRange {
			unit.GatherProgress++
			if int64(unit.GatherProgress) >= w.config.Economy.GatherIntervalTicks {
				unit.GatherProgress = 0
				var remaining int
				err := tx.QueryRow(ctx, `UPDATE resource_deposits SET amount=amount-1 WHERE id=$1 AND amount>0 RETURNING amount`, deposit.ID).Scan(&remaining)
				if err == nil {
					amounts[unit.OwnerID]++
				} else if err != pgx.ErrNoRows {
					return err
				}
			}
		} else {
			unit.GatherProgress = 0
		}
		if _, err := tx.Exec(ctx, `UPDATE units SET gather_target_id=$2,gather_progress=$3 WHERE id=$1`, unit.ID, unit.GatherTargetID, unit.GatherProgress); err != nil {
			return err
		}
	}
	for playerID, gained := range amounts {
		var balance int64
		if err := tx.QueryRow(ctx, `UPDATE player_resources SET materials=materials+$2 WHERE player_id=$1 RETURNING materials`, playerID, gained).Scan(&balance); err != nil {
			return err
		}
		if err := appendWorldEvent(ctx, tx, Update{PlayerID: playerID, Tick: tick, Type: "resources.changed", Resources: []Resource{{Kind: "materials", Amount: balance}}}); err != nil {
			return err
		}
	}
	return nil
}
