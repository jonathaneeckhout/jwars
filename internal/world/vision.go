package world

type entityKey string

func (w *World) unitDefinition(kind string) UnitDefinition {
	return w.unitDefinitions[kind]
}

func (w *World) unitVisionRange(kind string) int {
	return w.unitDefinition(kind).VisionRange
}

func (w *World) unitMaxHealth(kind string) int {
	return w.unitDefinition(kind).MaxHealth
}

func (w *World) unitAttackDamage(kind string) int {
	return w.unitDefinition(kind).AttackDamage
}

func (w *World) unitAttackRange(kind string) int {
	return w.unitDefinition(kind).AttackRange
}

func (w *World) buildingDefinition(kind string) BuildingDefinition {
	return w.definitions[kind]
}

func (w *World) buildingVisionRange(building Building) int {
	if building.Status != "complete" {
		return 0
	}
	return w.buildingDefinition(building.Kind).VisionRange
}

func (w *World) enrichUnit(unit Unit, viewerID string) Unit {
	unit.MaxHealth = w.unitMaxHealth(unit.Kind)
	unit.VisionRange = w.unitVisionRange(unit.Kind)
	if unit.OwnerID != viewerID {
		unit.TargetX, unit.TargetY, unit.AttackTargetID, unit.GatherTargetID = nil, nil, nil, nil
		unit.GatherProgress = 0
	}
	return unit
}

func (w *World) enrichBuilding(building Building) Building {
	building.VisionRange = w.buildingVisionRange(building)
	return building
}

func (w *World) visibleEntities(playerID string, units []Unit, buildings []Building) map[entityKey]bool {
	visible := make(map[entityKey]bool, len(units)+len(buildings))
	for _, unit := range units {
		if unit.OwnerID == playerID {
			visible[entityKey("unit:"+unit.ID)] = true
		}
	}
	for _, building := range buildings {
		if building.OwnerID == playerID {
			visible[entityKey("building:"+building.ID)] = true
		}
	}

	for _, enemy := range units {
		key := entityKey("unit:" + enemy.ID)
		if visible[key] {
			continue
		}
		for _, observer := range units {
			if observer.OwnerID == playerID && chebyshev(observer.X, observer.Y, enemy.X, enemy.Y) <= w.unitVisionRange(observer.Kind) {
				visible[key] = true
				break
			}
		}
		if visible[key] {
			continue
		}
		for _, observer := range buildings {
			if observer.OwnerID == playerID && w.buildingVisionRange(observer) > 0 && chebyshevToBuilding(enemy.X, enemy.Y, observer) <= w.buildingVisionRange(observer) {
				visible[key] = true
				break
			}
		}
	}
	for _, enemy := range buildings {
		key := entityKey("building:" + enemy.ID)
		if visible[key] {
			continue
		}
		for _, observer := range units {
			if observer.OwnerID == playerID && chebyshevToBuilding(observer.X, observer.Y, enemy) <= w.unitVisionRange(observer.Kind) {
				visible[key] = true
				break
			}
		}
		if visible[key] {
			continue
		}
		for _, observer := range buildings {
			if observer.OwnerID == playerID && w.buildingVisionRange(observer) > 0 && buildingDistance(observer, enemy) <= w.buildingVisionRange(observer) {
				visible[key] = true
				break
			}
		}
	}
	return visible
}

func entityReference(key entityKey) EntityRef {
	if len(key) > len("unit:") && key[:len("unit:")] == "unit:" {
		return EntityRef{ID: string(key[len("unit:"):]), Type: "unit"}
	}
	return EntityRef{ID: string(key[len("building:"):]), Type: "building"}
}

func chebyshev(x1, y1, x2, y2 int) int {
	dx, dy := abs(x1-x2), abs(y1-y2)
	if dx > dy {
		return dx
	}
	return dy
}

func abs(value int) int {
	if value < 0 {
		return -value
	}
	return value
}

func distanceToBuilding(x, y, buildingX, buildingY, width, height int) int {
	nearestX := clamp(x, buildingX, buildingX+width-1)
	nearestY := clamp(y, buildingY, buildingY+height-1)
	return chebyshev(x, y, nearestX, nearestY)
}

func chebyshevToBuilding(x, y int, building Building) int {
	return distanceToBuilding(x, y, building.X, building.Y, building.Width, building.Height)
}

func buildingDistance(a, b Building) int {
	dx := 0
	if a.X+a.Width-1 < b.X {
		dx = b.X - (a.X + a.Width - 1)
	} else if b.X+b.Width-1 < a.X {
		dx = a.X - (b.X + b.Width - 1)
	}
	dy := 0
	if a.Y+a.Height-1 < b.Y {
		dy = b.Y - (a.Y + a.Height - 1)
	} else if b.Y+b.Height-1 < a.Y {
		dy = a.Y - (b.Y + b.Height - 1)
	}
	if dx > dy {
		return dx
	}
	return dy
}

func clamp(value, min, max int) int {
	if value < min {
		return min
	}
	if value > max {
		return max
	}
	return value
}
