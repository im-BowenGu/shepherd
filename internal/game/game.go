// Package game contains the game rules for the current RoboCon competition.
//
// The 2027 game is Quidditch.  This package is deliberately independent of the
// hardware and web layers so the scoring and league rules can be tested on
// their own and reused by the referee tools, the web API and user-code helpers.
package game

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Team identifies one of the four teams in a 2027 Quidditch round.
type Team string

const (
	Red    Team = "RED"
	Yellow Team = "YELLOW"
	Green  Team = "GREEN"
	Blue   Team = "BLUE"
)

// Teams is the canonical team order used by the arena (red, yellow, green,
// blue).  It is also the order of the four corners/zone files.
var Teams = []Team{Red, Yellow, Green, Blue}

// Valid reports whether t is one of the four competition teams.
func (t Team) Valid() bool {
	_, ok := TeamIndex(t)
	return ok
}

// Index returns the zero-based team index (red=0 ... blue=3).
func (t Team) Index() int {
	if i, ok := TeamIndex(t); ok {
		return i
	}
	return 0
}

// TeamIndex returns the zero-based index for a team name, accepting both the
// canonical names and the T0..T3 aliases used by the robot library.
func TeamIndex(t Team) (int, bool) {
	switch Team(strings.ToUpper(string(t))) {
	case Red, "T0":
		return 0, true
	case Yellow, "T1":
		return 1, true
	case Green, "T2":
		return 2, true
	case Blue, "T3":
		return 3, true
	default:
		return 0, false
	}
}

// ParseTeam parses a team name or T0..T3 alias.
func ParseTeam(s string) (Team, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", false
	}
	if i, ok := TeamIndex(Team(s)); ok {
		return Teams[i], true
	}
	return "", false
}

// TeamForIndex returns the team with the given zero-based index.
func TeamForIndex(i int) (Team, bool) {
	if i < 0 || i >= len(Teams) {
		return "", false
	}
	return Teams[i], true
}

// TeamForZone maps a zone file number (zone1.txt ... zone4.txt) to a team.
// The zone files are 1-based; zone 0 is the historical "no zone" fallback and
// is treated as red.  Team names are also accepted so callers can pass either
// "1" or "RED".
func TeamForZone(zone string) Team {
	zone = strings.TrimSpace(zone)
	if team, ok := ParseTeam(zone); ok {
		return team
	}
	if n, err := strconv.Atoi(zone); err == nil {
		if n == 0 {
			return Red
		}
		if team, ok := TeamForIndex(n - 1); ok {
			return team
		}
	}
	return Red
}

// ZoneForTeam returns the 1-based zone file number for a team.
func ZoneForTeam(t Team) string {
	return strconv.Itoa(t.Index() + 1)
}

// MarkerType identifies the role of an AprilTag in the arena.
type MarkerType string

const (
	MarkerQuaffle MarkerType = "quaffle"
	MarkerSnitch  MarkerType = "snitch"
	MarkerArena   MarkerType = "arena"
)

// Marker describes one AprilTag used in the 2027 Quidditch arena.
type Marker struct {
	ID     int        `json:"id"`
	Type   MarkerType `json:"type"`
	Team   Team       `json:"team,omitempty"`
	Colour string     `json:"colour"`
	SizeMM int        `json:"size_mm"`
}

// Markers returns the complete 2027 Quidditch AprilTag map from the rules.
func Markers() []Marker {
	markers := make([]Marker, 0, 5+24)
	for i, team := range Teams {
		markers = append(markers, Marker{
			ID:     i,
			Type:   MarkerQuaffle,
			Team:   team,
			Colour: string(team),
			SizeMM: 100,
		})
	}
	markers = append(markers, Marker{
		ID:     4,
		Type:   MarkerSnitch,
		Colour: "GOLD",
		SizeMM: 100,
	})
	for id := 100; id <= 123; id++ {
		var team Team
		switch {
		case id <= 102 || id >= 121:
			team = Red
		case id <= 108:
			team = Yellow
		case id <= 114:
			team = Green
		default:
			team = Blue
		}
		markers = append(markers, Marker{
			ID:     id,
			Type:   MarkerArena,
			Team:   team,
			Colour: string(team),
			SizeMM: 200,
		})
	}
	return markers
}

// MarkerForID returns the marker definition for an AprilTag ID.
func MarkerForID(id int) (Marker, bool) {
	for _, m := range Markers() {
		if m.ID == id {
			return m, true
		}
	}
	return Marker{}, false
}

// Location is one of the three scoring locations in the arena.
type Location string

const (
	LocationNormal  Location = "normal"
	LocationHome    Location = "home"
	LocationBludger Location = "bludger"
)

// Valid reports whether l is a scoring location.
func (l Location) Valid() bool {
	switch l {
	case LocationNormal, LocationHome, LocationBludger:
		return true
	default:
		return false
	}
}

// Config is the mutable part of the game rules.  The zero value is not useful;
// use DefaultConfig and override fields from a JSON file when needed.
type Config struct {
	Name                      string          `json:"name"`
	Year                      int             `json:"year"`
	RoundLengthSeconds        int             `json:"round_length_seconds"`
	MaxTeamsPerRound          int             `json:"max_teams_per_round"`
	ScoreLockSeconds          int             `json:"score_lock_seconds"`
	SnitchDropEarliestSeconds int             `json:"snitch_drop_earliest_seconds"`
	SnitchDropLatestSeconds   int             `json:"snitch_drop_latest_seconds"`
	Zones                     map[string]Team `json:"zones"`
}

// DefaultConfig returns the 2027 Quidditch rule configuration.
func DefaultConfig() Config {
	return Config{
		Name:                      "Quidditch",
		Year:                      2027,
		RoundLengthSeconds:        180,
		MaxTeamsPerRound:          4,
		ScoreLockSeconds:          10,
		SnitchDropEarliestSeconds: 40,
		SnitchDropLatestSeconds:   20,
		Zones: map[string]Team{
			"0": Red,
			"1": Red,
			"2": Yellow,
			"3": Green,
			"4": Blue,
		},
	}
}

// Validate checks that the configuration can be used for a round.
func (c Config) Validate() error {
	if c.RoundLengthSeconds <= 0 {
		return fmt.Errorf("round_length_seconds must be positive")
	}
	if c.MaxTeamsPerRound <= 0 || c.MaxTeamsPerRound > len(Teams) {
		return fmt.Errorf("max_teams_per_round must be between 1 and %d", len(Teams))
	}
	if c.SnitchDropEarliestSeconds <= c.SnitchDropLatestSeconds {
		return fmt.Errorf("snitch drop window must open before it closes")
	}
	if c.SnitchDropLatestSeconds < 0 || c.SnitchDropEarliestSeconds > c.RoundLengthSeconds {
		return fmt.Errorf("snitch drop window is outside the round")
	}
	for zone, team := range c.Zones {
		if !team.Valid() {
			return fmt.Errorf("zone %q has invalid team %q", zone, team)
		}
	}
	return nil
}

// Load reads a game configuration from path, falling back to the built-in
// 2027 defaults when path is empty or does not exist.  Missing fields keep
// their default value because the defaults are decoded first.
func Load(path string) (Config, error) {
	cfg := DefaultConfig()
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, fmt.Errorf("read game config: %w", err)
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse game config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("invalid game config %s: %w", path, err)
	}
	return cfg, nil
}

// RoundLength returns the configured round duration.
func (c Config) RoundLength() time.Duration {
	return time.Duration(c.RoundLengthSeconds) * time.Second
}

// ScoreLock returns the delay between the end of a round and the start of
// scoring (10 seconds in the 2027 rules).
func (c Config) ScoreLock() time.Duration {
	return time.Duration(c.ScoreLockSeconds) * time.Second
}

// TeamForZone maps a zone file number to the team using this configuration.
func (c Config) TeamForZone(zone string) Team {
	if team, ok := c.Zones[strings.TrimSpace(zone)]; ok {
		return team
	}
	return TeamForZone(zone)
}

// Timing describes the round clock and the snitch drop window.
type Timing struct {
	RoundLengthSeconds          float64  `json:"round_length_seconds"`
	ElapsedSeconds              float64  `json:"elapsed_seconds"`
	TimeLeftSeconds             *float64 `json:"time_left_seconds,omitempty"`
	SnitchWindow                bool     `json:"snitch_window"`
	SnitchWindowOpensInSeconds  *float64 `json:"snitch_window_opens_in_seconds,omitempty"`
	SnitchWindowClosesInSeconds *float64 `json:"snitch_window_closes_in_seconds,omitempty"`
}

// TimingAt calculates the state of the round clock at the given elapsed time.
// The snitch window opens when the earliest drop point is reached and stays
// open until the latest drop point passes.
func (c Config) TimingAt(elapsed time.Duration) Timing {
	round := c.RoundLength()
	if elapsed < 0 {
		elapsed = 0
	}
	if elapsed > round {
		elapsed = round
	}
	remaining := round - elapsed

	timing := Timing{
		RoundLengthSeconds: round.Seconds(),
		ElapsedSeconds:     elapsed.Seconds(),
	}
	left := remaining.Seconds()
	timing.TimeLeftSeconds = &left

	earliest := time.Duration(c.SnitchDropEarliestSeconds) * time.Second
	latest := time.Duration(c.SnitchDropLatestSeconds) * time.Second

	switch {
	case remaining > earliest:
		opens := (remaining - earliest).Seconds()
		timing.SnitchWindowOpensInSeconds = &opens
	case remaining >= latest:
		timing.SnitchWindow = true
		closes := (remaining - latest).Seconds()
		timing.SnitchWindowClosesInSeconds = &closes
	}
	return timing
}
