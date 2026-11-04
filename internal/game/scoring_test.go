package game

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlacementPoints(t *testing.T) {
	cases := []struct {
		placement Placement
		team      Team
		want      int
	}{
		{Placement{Red, LocationNormal}, Red, 3},
		{Placement{Blue, LocationNormal}, Red, 2},
		{Placement{Red, LocationHome}, Red, 5},
		{Placement{Blue, LocationHome}, Red, 3},
		{Placement{Red, LocationBludger}, Red, 2},
		{Placement{Blue, LocationBludger}, Red, 1},
		{Placement{Red, Location("unknown")}, Red, 0},
	}
	for _, c := range cases {
		if got := PlacementPoints(c.placement, c.team); got != c.want {
			t.Errorf("PlacementPoints(%+v, %s) = %d, want %d", c.placement, c.team, got, c.want)
		}
	}
}

func TestScoreTeam(t *testing.T) {
	score := ScoreTeam(TeamRound{
		Team:             Red,
		LeftHomeArea:     true,
		PossessesQuaffle: true,
		Placements: []Placement{
			{Quaffle: Red, Location: LocationNormal},
			{Quaffle: Blue, Location: LocationHome},
		},
		Snitch: true,
	})

	if score.LeftHomeArea != 1 || score.Possession != 1 {
		t.Errorf("unexpected bonus points: %+v", score)
	}
	if score.QuaffleBase != 6 {
		t.Errorf("QuaffleBase = %d, want 6", score.QuaffleBase)
	}
	if score.SnitchMultiplier != 3 || score.QuafflePoints != 18 {
		t.Errorf("snitch scoring = %+v, want multiplier 3 and 18 points", score)
	}
	if score.Total != 20 {
		t.Errorf("Total = %d, want 20", score.Total)
	}
}

func TestScoreTeamBludgerPenalty(t *testing.T) {
	score := ScoreTeam(TeamRound{
		Team:              Red,
		MajorityInBludger: true,
		Placements:        []Placement{{Quaffle: Red, Location: LocationNormal}},
	})
	if score.Penalty != 1 || score.QuafflePoints != 2 {
		t.Errorf("expected penalty to reduce 3 to 2, got %+v", score)
	}

	zero := ScoreTeam(TeamRound{Team: Red, MajorityInBludger: true})
	if zero.Penalty != 0 || zero.Total != 0 {
		t.Errorf("penalty must not apply below zero: %+v", zero)
	}
}

func TestLeagueTableTies(t *testing.T) {
	scores := []TeamScore{
		{Team: Red, Total: 10},
		{Team: Yellow, Total: 10},
		{Team: Green, Total: 5},
		{Team: Blue, Total: 1},
	}
	table := LeagueTable(scores)
	want := map[Team]struct {
		rank   int
		points int
	}{
		Red:    {1, 7},
		Yellow: {1, 7},
		Green:  {3, 4},
		Blue:   {4, 2},
	}
	for _, entry := range table {
		w := want[entry.Team]
		if entry.Rank != w.rank || entry.LeaguePoints != w.points {
			t.Errorf("%s: got rank %d points %d, want rank %d points %d",
				entry.Team, entry.Rank, entry.LeaguePoints, w.rank, w.points)
		}
	}
}

func TestLeagueTableThreeWayTie(t *testing.T) {
	table := LeagueTable([]TeamScore{
		{Team: Red, Total: 10},
		{Team: Yellow, Total: 10},
		{Team: Green, Total: 10},
		{Team: Blue, Total: 1},
	})
	for _, entry := range table {
		switch entry.Team {
		case Red, Yellow, Green:
			if entry.Rank != 1 || entry.LeaguePoints != 6 {
				t.Errorf("%s: got rank %d points %d, want rank 1 points 6",
					entry.Team, entry.Rank, entry.LeaguePoints)
			}
		case Blue:
			if entry.Rank != 4 || entry.LeaguePoints != 2 {
				t.Errorf("Blue: got rank %d points %d, want rank 4 points 2",
					entry.Rank, entry.LeaguePoints)
			}
		}
	}
}

func TestLeagueTableDisqualified(t *testing.T) {
	table := LeagueTable([]TeamScore{
		{Team: Red, Total: 10, Disqualified: true},
		{Team: Yellow, Total: 8},
		{Team: Green, Total: 4},
	})
	for _, entry := range table[:2] {
		if entry.Team == Red {
			t.Fatal("disqualified team should sort after valid teams")
		}
	}
	if table[0].Team != Yellow || table[0].Rank != 1 || table[0].LeaguePoints != 8 {
		t.Errorf("unexpected first place: %+v", table[0])
	}
	if table[1].Team != Green || table[1].Rank != 2 || table[1].LeaguePoints != 6 {
		t.Errorf("unexpected second place: %+v", table[1])
	}
	if table[2].Team != Red || table[2].Rank != 0 || table[2].LeaguePoints != 0 {
		t.Errorf("disqualified team should get zero points: %+v", table[2])
	}
}

func TestAdvanceKnockout(t *testing.T) {
	advancing := AdvanceKnockout([]KnockoutEntry{
		{Team: Red, Score: 10, LeagueRank: 4},
		{Team: Yellow, Score: 12, LeagueRank: 3},
		{Team: Green, Score: 10, LeagueRank: 1},
		{Team: Blue, Score: 20, LeagueRank: 2, Disqualified: true},
	})
	if len(advancing) != 2 || advancing[0] != Yellow || advancing[1] != Green {
		t.Errorf("AdvanceKnockout = %v, want [YELLOW GREEN]", advancing)
	}
}

func TestTimingAt(t *testing.T) {
	cfg := DefaultConfig()

	start := cfg.TimingAt(0)
	if start.TimeLeftSeconds == nil || *start.TimeLeftSeconds != 180 {
		t.Errorf("start timing = %+v", start)
	}
	if start.SnitchWindow {
		t.Error("snitch window must be closed at the start")
	}
	if start.SnitchWindowOpensInSeconds == nil || *start.SnitchWindowOpensInSeconds != 140 {
		t.Errorf("snitch opens in = %v, want 140", start.SnitchWindowOpensInSeconds)
	}

	open := cfg.TimingAt(140 * time.Second)
	if !open.SnitchWindow {
		t.Errorf("snitch window should be open at 140s: %+v", open)
	}
	if open.SnitchWindowClosesInSeconds == nil || *open.SnitchWindowClosesInSeconds != 20 {
		t.Errorf("snitch closes in = %v, want 20", open.SnitchWindowClosesInSeconds)
	}

	closed := cfg.TimingAt(161 * time.Second)
	if closed.SnitchWindow {
		t.Errorf("snitch window should be closed at 161s: %+v", closed)
	}
}

func TestMarkers(t *testing.T) {
	quaffle, ok := MarkerForID(0)
	if !ok || quaffle.Type != MarkerQuaffle || quaffle.Team != Red || quaffle.SizeMM != 100 {
		t.Errorf("marker 0 = %+v, ok=%v", quaffle, ok)
	}
	snitch, ok := MarkerForID(4)
	if !ok || snitch.Type != MarkerSnitch || snitch.SizeMM != 100 {
		t.Errorf("marker 4 = %+v, ok=%v", snitch, ok)
	}
	wall, ok := MarkerForID(103)
	if !ok || wall.Type != MarkerArena || wall.Team != Yellow || wall.SizeMM != 200 {
		t.Errorf("marker 103 = %+v, ok=%v", wall, ok)
	}
	if _, ok := MarkerForID(99); ok {
		t.Error("marker 99 should not exist")
	}
	if len(Markers()) != 29 {
		t.Errorf("len(Markers()) = %d, want 29", len(Markers()))
	}
}

func TestTeamForZone(t *testing.T) {
	cases := map[string]Team{
		"0":     Red,
		"1":     Red,
		"2":     Yellow,
		"3":     Green,
		"4":     Blue,
		"red":   Red,
		"T2":    Green,
		"bogus": Red,
	}
	for zone, want := range cases {
		if got := TeamForZone(zone); got != want {
			t.Errorf("TeamForZone(%q) = %s, want %s", zone, got, want)
		}
	}
	if ZoneForTeam(Blue) != "4" {
		t.Errorf("ZoneForTeam(Blue) = %q, want 4", ZoneForTeam(Blue))
	}
}

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "game.json")
	data, _ := json.Marshal(map[string]any{
		"round_length_seconds":         120,
		"snitch_drop_earliest_seconds": 30,
		"snitch_drop_latest_seconds":   15,
		"zones":                        map[string]string{"1": "BLUE"},
	})
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RoundLengthSeconds != 120 || cfg.SnitchDropEarliestSeconds != 30 {
		t.Errorf("config override failed: %+v", cfg)
	}
	if cfg.TeamForZone("1") != Blue {
		t.Errorf("zone override failed: %s", cfg.TeamForZone("1"))
	}
	if cfg.TeamForZone("2") != Yellow {
		t.Errorf("default zone lost during merge: %s", cfg.TeamForZone("2"))
	}
}
