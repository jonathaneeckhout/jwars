package world

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type UnitDefinition struct {
	Kind             string `json:"kind"`
	MaxHealth        int    `json:"max_health"`
	VisionRange      int    `json:"vision_range"`
	AttackDamage     int    `json:"attack_damage"`
	AttackRange      int    `json:"attack_range"`
	TrainingCost     int64  `json:"training_cost"`
	TrainingTicks    int64  `json:"training_ticks"`
	TrainingBuilding string `json:"training_building,omitempty"`
}

type EconomySettings struct {
	StartingMaterials                int64   `json:"starting_materials"`
	GatherIntervalTicks              int64   `json:"gather_interval_ticks"`
	GatherRange                      int     `json:"gather_range"`
	DepositCapacity                  int     `json:"deposit_capacity"`
	DepositRegenerationIntervalTicks int64   `json:"deposit_regeneration_interval_ticks"`
	DepositRegenerationAmount        int     `json:"deposit_regeneration_amount"`
	DepositLocations                 []Point `json:"deposit_locations"`
}

type StartingUnit struct {
	Kind string `json:"kind"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
}

type WorldSettings struct {
	MapSize               int            `json:"map_size"`
	TickIntervalMillis    int            `json:"tick_interval_millis"`
	Hill                  Point          `json:"hill"`
	HillRadius            int            `json:"hill_radius"`
	TicksPerControlPoint  int64          `json:"ticks_per_control_point"`
	SeasonDurationSeconds int64          `json:"season_duration_seconds"`
	SeasonStartWeekday    string         `json:"season_start_weekday"`
	SeasonStartTimeUTC    string         `json:"season_start_time_utc"`
	EnrollmentHours       int            `json:"enrollment_hours"`
	MaxPlayers            int            `json:"max_players"`
	SpawnLocations        []Point        `json:"spawn_locations"`
	StartingUnits         []StartingUnit `json:"starting_units"`
}

type GameConfig struct {
	Buildings []BuildingDefinition `json:"buildings"`
	Units     []UnitDefinition     `json:"units"`
	Economy   EconomySettings      `json:"economy"`
	World     WorldSettings        `json:"world"`
}

func LoadConfig(directory string) (GameConfig, error) {
	var config GameConfig
	for _, file := range []struct {
		name     string
		target   any
		required []string
	}{
		{name: "buildings.json", required: []string{"buildings"}, target: &struct {
			Buildings *[]BuildingDefinition `json:"buildings"`
		}{Buildings: &config.Buildings}},
		{name: "units.json", required: []string{"units"}, target: &struct {
			Units *[]UnitDefinition `json:"units"`
		}{Units: &config.Units}},
		{name: "economy.json", required: []string{"starting_materials", "gather_interval_ticks", "gather_range", "deposit_capacity", "deposit_regeneration_interval_ticks", "deposit_regeneration_amount", "deposit_locations"}, target: &config.Economy},
		{name: "world.json", required: []string{"map_size", "tick_interval_millis", "hill", "hill_radius", "ticks_per_control_point", "season_duration_seconds", "season_start_weekday", "season_start_time_utc", "enrollment_hours", "max_players", "spawn_locations", "starting_units"}, target: &config.World},
	} {
		content, err := os.ReadFile(filepath.Join(directory, file.name))
		if err != nil {
			return GameConfig{}, fmt.Errorf("read variables file %s: %w", file.name, err)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(content, &fields); err != nil {
			return GameConfig{}, fmt.Errorf("decode variables file %s: %w", file.name, err)
		}
		for _, key := range file.required {
			if _, exists := fields[key]; !exists {
				return GameConfig{}, fmt.Errorf("variables file %s is missing required setting %q", file.name, key)
			}
		}
		decoder := json.NewDecoder(bytes.NewReader(content))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(file.target); err != nil {
			return GameConfig{}, fmt.Errorf("decode variables file %s: %w", file.name, err)
		}
	}
	if err := config.Validate(); err != nil {
		return GameConfig{}, err
	}
	return config, nil
}

func (c GameConfig) Validate() error {
	if c.World.MapSize < 1 || c.World.TickIntervalMillis < 1 || c.World.TicksPerControlPoint < 1 || c.World.SeasonDurationSeconds < 1 {
		return fmt.Errorf("world map size, tick interval, scoring interval, and season duration must be positive")
	}
	if c.World.SeasonDurationSeconds != int64((7 * 24 * time.Hour).Seconds()) {
		return fmt.Errorf("season duration must be exactly seven days")
	}
	if _, err := time.Parse("15:04", c.World.SeasonStartTimeUTC); err != nil {
		return fmt.Errorf("season_start_time_utc must use 24-hour HH:MM format")
	}
	if weekday, ok := parseWeekday(c.World.SeasonStartWeekday); !ok || strings.ToLower(weekday.String()) != c.World.SeasonStartWeekday {
		return fmt.Errorf("season_start_weekday must be a lowercase weekday name")
	}
	if c.World.EnrollmentHours < 1 || c.World.EnrollmentHours >= 168 || c.World.MaxPlayers < 1 || c.World.MaxPlayers > len(c.World.SpawnLocations) {
		return fmt.Errorf("world enrollment hours, max players, and spawn locations are invalid")
	}
	if !c.World.contains(c.World.Hill.X, c.World.Hill.Y) || c.World.HillRadius < 0 {
		return fmt.Errorf("world hill must be within the map and have a non-negative radius")
	}
	if c.Economy.StartingMaterials < 0 || c.Economy.GatherIntervalTicks < 1 || c.Economy.GatherRange < 0 || c.Economy.DepositCapacity < 1 || c.Economy.DepositRegenerationIntervalTicks < 1 || c.Economy.DepositRegenerationAmount < 1 {
		return fmt.Errorf("economy settings contain an invalid amount, range, or interval")
	}

	units := make(map[string]UnitDefinition, len(c.Units))
	for _, unit := range c.Units {
		if unit.Kind == "" || unit.MaxHealth < 1 || unit.VisionRange < 0 || unit.AttackDamage < 0 || unit.AttackRange < 0 || unit.TrainingCost < 0 || unit.TrainingTicks < 0 {
			return fmt.Errorf("invalid unit definition %q", unit.Kind)
		}
		if _, exists := units[unit.Kind]; exists {
			return fmt.Errorf("duplicate unit definition %q", unit.Kind)
		}
		if (unit.TrainingTicks == 0) != (unit.TrainingBuilding == "") || (unit.TrainingTicks == 0 && unit.TrainingCost != 0) {
			return fmt.Errorf("unit %q has incomplete training settings", unit.Kind)
		}
		if unit.AttackDamage == 0 && unit.AttackRange != 0 {
			return fmt.Errorf("unit %q has an attack range but no attack damage", unit.Kind)
		}
		units[unit.Kind] = unit
	}
	if len(units) == 0 {
		return fmt.Errorf("at least one unit definition is required")
	}
	for _, kind := range []string{"worker", "soldier"} {
		if _, exists := units[kind]; !exists {
			return fmt.Errorf("required unit definition %q is missing", kind)
		}
	}

	buildings := make(map[string]BuildingDefinition, len(c.Buildings))
	for _, building := range c.Buildings {
		if building.Kind == "" || building.Width < 1 || building.Height < 1 || building.Width > c.World.MapSize || building.Height > c.World.MapSize || building.Cost < 0 || building.BuildTicks < 0 || building.VisionRange < 0 || building.AttackDamage < 0 || building.AttackRange < 0 || building.AttackIntervalTicks < 0 || building.MaxHealth < 0 {
			return fmt.Errorf("invalid building definition %q", building.Kind)
		}
		if building.Destructible != (building.MaxHealth > 0) || (building.Starting && building.Destructible) {
			return fmt.Errorf("building %q must have positive max_health exactly when destructible, and starting buildings must be indestructible", building.Kind)
		}
		if _, exists := buildings[building.Kind]; exists {
			return fmt.Errorf("duplicate building definition %q", building.Kind)
		}
		if building.Starting {
			if building.StartX < 0 || building.StartY < 0 || building.StartX+building.Width > c.World.MapSize || building.StartY+building.Height > c.World.MapSize {
				return fmt.Errorf("starting building %q is outside the map", building.Kind)
			}
		} else if building.BuildTicks < 1 {
			return fmt.Errorf("buildable building %q must have positive build_ticks", building.Kind)
		}
		if building.AttackDamage > 0 && (building.AttackRange < 1 || building.AttackIntervalTicks < 1) {
			return fmt.Errorf("attacking building %q needs a positive range and interval", building.Kind)
		}
		buildings[building.Kind] = building
	}
	if len(buildings) == 0 {
		return fmt.Errorf("at least one building definition is required")
	}
	for _, unit := range units {
		if unit.TrainingTicks > 0 {
			building, exists := buildings[unit.TrainingBuilding]
			if !exists || (building.Starting && unit.Kind != "worker") {
				return fmt.Errorf("unit %q has an unknown or unsupported training building %q", unit.Kind, unit.TrainingBuilding)
			}
		}
	}
	startingBuilding, hasStartingBuilding := BuildingDefinition{}, false
	for _, building := range c.Buildings {
		if building.Starting {
			startingBuilding, hasStartingBuilding = building, true
			break
		}
	}
	if !hasStartingBuilding {
		return fmt.Errorf("at least one starting building is required")
	}
	spawnSeen := make(map[Point]bool, len(c.World.SpawnLocations))
	spawnDistance := -1
	for _, spawn := range c.World.SpawnLocations {
		if spawn.X < 0 || spawn.Y < 0 || spawn.X+startingBuilding.StartX+startingBuilding.Width > c.World.MapSize || spawn.Y+startingBuilding.StartY+startingBuilding.Height > c.World.MapSize || spawnSeen[spawn] {
			return fmt.Errorf("invalid or duplicate spawn location at (%d,%d)", spawn.X, spawn.Y)
		}
		spawnSeen[spawn] = true
		distance := max(abs(spawn.X-c.World.Hill.X), abs(spawn.Y-c.World.Hill.Y))
		if spawnDistance < 0 {
			spawnDistance = distance
		} else if distance != spawnDistance {
			return fmt.Errorf("spawn locations must be equally distant from the hill")
		}
	}
	for _, unit := range c.World.StartingUnits {
		if _, exists := units[unit.Kind]; !exists || unit.X < 0 || unit.Y < 0 || unit.X >= c.World.MapSize || unit.Y >= c.World.MapSize {
			return fmt.Errorf("invalid starting unit offset %q at (%d,%d)", unit.Kind, unit.X, unit.Y)
		}
		for _, spawn := range c.World.SpawnLocations {
			if !c.World.contains(spawn.X+unit.X, spawn.Y+unit.Y) {
				return fmt.Errorf("starting unit %q is outside the map at spawn (%d,%d)", unit.Kind, spawn.X, spawn.Y)
			}
		}
	}
	if len(c.World.StartingUnits) == 0 {
		return fmt.Errorf("at least one starting unit is required")
	}
	if len(c.Economy.DepositLocations) == 0 {
		return fmt.Errorf("at least one resource deposit location is required")
	}
	seenDeposits := make(map[Point]bool, len(c.Economy.DepositLocations))
	for _, point := range c.Economy.DepositLocations {
		if !c.World.contains(point.X, point.Y) || seenDeposits[point] {
			return fmt.Errorf("invalid or duplicate resource deposit at (%d,%d)", point.X, point.Y)
		}
		seenDeposits[point] = true
	}
	for _, spawn := range c.World.SpawnLocations {
		nearest := c.World.MapSize
		for _, deposit := range c.Economy.DepositLocations {
			distance := max(abs(spawn.X-deposit.X), abs(spawn.Y-deposit.Y))
			if distance < nearest {
				nearest = distance
			}
		}
		if nearest > 10 {
			return fmt.Errorf("spawn location at (%d,%d) has no nearby material deposit", spawn.X, spawn.Y)
		}
	}
	return nil
}

func parseWeekday(value string) (time.Weekday, bool) {
	for day := time.Sunday; day <= time.Saturday; day++ {
		if strings.EqualFold(day.String(), value) {
			return day, true
		}
	}
	return 0, false
}

func (w WorldSettings) contains(x, y int) bool {
	return x >= 0 && x < w.MapSize && y >= 0 && y < w.MapSize
}
