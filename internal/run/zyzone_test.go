package run

import "testing"

func TestColourForZone(t *testing.T) {
	tests := map[string]string{
		"0": "RED",
		"1": "RED",
		"2": "YELLOW",
		"3": "GREEN",
		"4": "BLUE",
		"":  "RED",
		"x": "RED",
	}
	for zone, want := range tests {
		if got := colourForZone(zone); got != want {
			t.Errorf("colourForZone(%q) = %q, want %q", zone, got, want)
		}
	}
}
