package gpio

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectZone(t *testing.T) {
	usb := t.TempDir()
	b := &Button{arenaUSB: usb}

	if got := b.detectZone(); got != "0" {
		t.Errorf("empty USB: got %q, want 0", got)
	}

	write(t, filepath.Join(usb, "zone2.txt"), "")
	if got := b.detectZone(); got != "2" {
		t.Errorf("zone2.txt: got %q, want 2", got)
	}

	// zone1 should win over zone2 (checked first)
	write(t, filepath.Join(usb, "zone1.txt"), "")
	if got := b.detectZone(); got != "1" {
		t.Errorf("zone1+zone2: got %q, want 1", got)
	}

	// 2027: a 4th zone file exists
	usb4 := t.TempDir()
	write(t, filepath.Join(usb4, "zone4.txt"), "")
	if got := (&Button{arenaUSB: usb4}).detectZone(); got != "4" {
		t.Errorf("zone4.txt: got %q, want 4", got)
	}
}

func TestDetectArena(t *testing.T) {
	usb := t.TempDir()
	b := &Button{arenaUSB: usb}

	if got := b.detectArena(); got != "A" {
		t.Errorf("empty USB: got %q, want A (default)", got)
	}

	write(t, filepath.Join(usb, "arenaB.txt"), "")
	if got := b.detectArena(); got != "B" {
		t.Errorf("arenaB.txt: got %q, want B", got)
	}

	write(t, filepath.Join(usb, "arenaA.txt"), "")
	if got := b.detectArena(); got != "A" {
		t.Errorf("arenaA+arenaB: got %q, want A (checked first)", got)
	}

	// lowercase variant
	usb2 := t.TempDir()
	write(t, filepath.Join(usb2, "arenab.txt"), "")
	b2 := &Button{arenaUSB: usb2}
	if got := b2.detectArena(); got != "B" {
		t.Errorf("arenab.txt: got %q, want B", got)
	}

	if got := DetectArena(usb); got != "A" {
		t.Errorf("DetectArena: got %q, want A", got)
	}
}
