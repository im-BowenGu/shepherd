package game

import (
	"sort"
	"strings"
)

// Placement is one Quaffle in a team's scoring area.
type Placement struct {
	// Quaffle is the colour of the Quaffle, not the team that owns the area.
	Quaffle Team `json:"quaffle"`
	// Location is the scoring location containing the Quaffle.
	Location Location `json:"location"`
}

// TeamRound is the state of one robot and its scoring area at the end of a
// round.  The fields map directly to the conditions in the 2027 rulebook.
type TeamRound struct {
	Team              Team        `json:"team"`
	Disqualified      bool        `json:"disqualified"`
	LeftHomeArea      bool        `json:"left_home_area"`
	PossessesQuaffle  bool        `json:"possesses_quaffle"`
	Placements        []Placement `json:"placements"`
	Snitch            bool        `json:"snitch"`
	MajorityInBludger bool        `json:"majority_in_bludger"`
}

// TeamScore is the result of scoring one robot's round.
type TeamScore struct {
	Team             Team `json:"team"`
	Disqualified     bool `json:"disqualified"`
	LeftHomeArea     int  `json:"left_home_area_points"`
	Possession       int  `json:"possession_points"`
	QuaffleBase      int  `json:"quaffle_base_points"`
	SnitchMultiplier int  `json:"snitch_multiplier"`
	QuafflePoints    int  `json:"quaffle_points"`
	Penalty          int  `json:"penalty"`
	Total            int  `json:"total"`
}

// PlacementPoints returns the rulebook value for a Quaffle placed in a scoring
// location.  A team's own-colour Quaffle scores more than any other colour.
func PlacementPoints(placement Placement, team Team) int {
	own := placement.Quaffle == team
	switch placement.Location {
	case LocationNormal:
		if own {
			return 3
		}
		return 2
	case LocationHome:
		if own {
			return 5
		}
		return 3
	case LocationBludger:
		if own {
			return 2
		}
		return 1
	default:
		return 0
	}
}

// ScoreTeam scores a single robot's end-of-round state.
func ScoreTeam(round TeamRound) TeamScore {
	score := TeamScore{Team: round.Team, Disqualified: round.Disqualified}

	if round.LeftHomeArea {
		score.LeftHomeArea = 1
	}
	if round.PossessesQuaffle {
		score.Possession = 1
	}
	for _, placement := range round.Placements {
		score.QuaffleBase += PlacementPoints(placement, round.Team)
	}

	score.QuafflePoints = score.QuaffleBase
	if round.Snitch {
		// The Snitch multiplies all points accrued from Quaffles by three.
		score.SnitchMultiplier = 3
		score.QuafflePoints *= score.SnitchMultiplier
	}
	if round.MajorityInBludger && score.QuafflePoints > 0 {
		// More than half the robot in its own Bludger zone costs one Quaffle
		// point, but never takes the Quaffle score below zero.
		score.Penalty = 1
		score.QuafflePoints--
	}

	score.Total = score.LeftHomeArea + score.Possession + score.QuafflePoints
	return score
}

// ScoreRound scores every team in a round, preserving input order.
func ScoreRound(rounds []TeamRound) []TeamScore {
	scores := make([]TeamScore, 0, len(rounds))
	for _, round := range rounds {
		scores = append(scores, ScoreTeam(round))
	}
	return scores
}

// LeagueEntry is a team's position after applying league points.
type LeagueEntry struct {
	Team         Team `json:"team"`
	RoundScore   int  `json:"round_score"`
	LeaguePoints int  `json:"league_points"`
	Rank         int  `json:"rank"`
	Disqualified bool `json:"disqualified"`
}

// leaguePointsForPlace returns the league points for a 1-based finishing
// position: 8, 6, 4, 2 for first through fourth, then zero.
func leaguePointsForPlace(place int) int {
	switch place {
	case 1:
		return 8
	case 2:
		return 6
	case 3:
		return 4
	case 4:
		return 2
	default:
		return 0
	}
}

// LeagueTable applies the 2027 league points and tie rules to a round.
//
// The input may contain exactly one score per team.  Tied teams receive the
// mean of the points for the places they occupy (for example, joint first
// receives (8+6)/2 = 7).  Disqualified teams always receive zero league
// points.
func LeagueTable(scores []TeamScore) []LeagueEntry {
	valid := make([]LeagueEntry, 0, len(scores))
	disqualified := make([]LeagueEntry, 0, len(scores))
	for _, score := range scores {
		entry := LeagueEntry{
			Team:         score.Team,
			RoundScore:   score.Total,
			Disqualified: score.Disqualified,
		}
		if score.Disqualified {
			disqualified = append(disqualified, entry)
			continue
		}
		valid = append(valid, entry)
	}

	sort.SliceStable(valid, func(i, j int) bool {
		if valid[i].RoundScore != valid[j].RoundScore {
			return valid[i].RoundScore > valid[j].RoundScore
		}
		return valid[i].Team < valid[j].Team
	})

	for i := 0; i < len(valid); {
		j := i
		for j < len(valid) && valid[j].RoundScore == valid[i].RoundScore {
			j++
		}

		firstPlace := i + 1
		lastPlace := j
		points := 0
		for p := firstPlace; p <= lastPlace; p++ {
			points += leaguePointsForPlace(p)
		}
		points /= j - i

		for k := i; k < j; k++ {
			valid[k].Rank = firstPlace
			valid[k].LeaguePoints = points
		}
		i = j
	}

	sort.SliceStable(disqualified, func(i, j int) bool {
		return disqualified[i].Team < disqualified[j].Team
	})
	return append(valid, disqualified...)
}

// KnockoutEntry is a team's result in a knockout heat.
type KnockoutEntry struct {
	Team         Team `json:"team"`
	Score        int  `json:"score"`
	LeagueRank   int  `json:"league_rank"`
	Disqualified bool `json:"disqualified"`
}

// AdvanceKnockout returns the two teams that progress from a knockout heat.
// Teams are ordered by score, then by their league seed (lower rank wins a
// tie).  Disqualified teams never progress.
func AdvanceKnockout(entries []KnockoutEntry) []Team {
	ordered := make([]KnockoutEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.Disqualified {
			ordered = append(ordered, entry)
		}
	}

	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Score != ordered[j].Score {
			return ordered[i].Score > ordered[j].Score
		}
		// A zero rank means "unseeded"; put unseeded teams after seeded ones.
		if ordered[i].LeagueRank != ordered[j].LeagueRank {
			if ordered[i].LeagueRank == 0 {
				return false
			}
			if ordered[j].LeagueRank == 0 {
				return true
			}
			return ordered[i].LeagueRank < ordered[j].LeagueRank
		}
		return strings.Compare(string(ordered[i].Team), string(ordered[j].Team)) < 0
	})

	advancing := make([]Team, 0, 2)
	for _, entry := range advancingLimit(ordered) {
		advancing = append(advancing, entry.Team)
	}
	return advancing
}

func advancingLimit(entries []KnockoutEntry) []KnockoutEntry {
	if len(entries) > 2 {
		return entries[:2]
	}
	return entries
}

// ScoringRule describes one row of the scoring table for the web API and the
// documentation.  It is not used by the scoring code itself.
type ScoringRule struct {
	Location Location `json:"location"`
	Own      int      `json:"own_colour_quaffle"`
	Other    int      `json:"other_colour_quaffle"`
}

// ScoringRules returns the Quaffle scoring table from the rulebook.
func ScoringRules() []ScoringRule {
	return []ScoringRule{
		{Location: LocationNormal, Own: 3, Other: 2},
		{Location: LocationHome, Own: 5, Other: 3},
		{Location: LocationBludger, Own: 2, Other: 1},
	}
}
