package sysinfo

import (
	"strings"
	"testing"

	"github.com/RoboConOxfordshire/shepherd/internal/hardware"
)

func TestCollectRender(t *testing.T) {
	info := Collect(hardware.Status{
		LevelShifter:    true,
		LevelShifterPin: hardware.LevelShifterPin,
		StartButtonPin:  hardware.StartButtonPin,
		StartButtonOK:   true,
		BoardPresent:    true,
		BoardVersion:    14,
	})
	out := info.Render()

	for _, want := range []string{
		"=== shepherd", "host", "memory", "end of report",
		"HARDWARE REPORT", "Robocon Board : Yes (v14", "level shifter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q\n%s", want, out)
		}
	}
}

func TestCollectWarnsWhenLevelShifterOff(t *testing.T) {
	info := Collect(hardware.Status{
		LevelShifterPin: hardware.LevelShifterPin,
		StartButtonPin:  hardware.StartButtonPin,
	})
	if !strings.Contains(strings.Join(info.Warnings, "\n"), "level shifter") {
		t.Errorf("expected level shifter warning, got %v", info.Warnings)
	}
}

func TestCollectWarnsWhenBoardAbsent(t *testing.T) {
	info := Collect(hardware.Status{
		LevelShifter:    true,
		LevelShifterPin: hardware.LevelShifterPin,
		StartButtonPin:  hardware.StartButtonPin,
		KeepaliveFails:  3,
		LastError:       "read version: input/output error",
	})
	joined := strings.Join(info.Warnings, "\n")
	if !strings.Contains(joined, "input/output error") {
		t.Errorf("expected board error in warnings, got %v", info.Warnings)
	}
	if !strings.Contains(joined, "keepalive") {
		t.Errorf("expected keepalive warning, got %v", info.Warnings)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[uint64]string{
		512:               "512 B",
		1024:              "1.0 KiB",
		1024 * 1024:       "1.0 MiB",
		256 * 1024 * 1024: "256.0 MiB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}
