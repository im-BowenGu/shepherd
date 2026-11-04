package graphics

import (
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/RoboConOxfordshire/shepherd/internal/config"
)

func testJPEG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 8), B: 200, A: 255})
		}
	}
	if err := jpeg.Encode(f, img, nil); err != nil {
		t.Fatal(err)
	}
}

func TestSelectStartGraphic(t *testing.T) {
	root := t.TempDir()
	arenaUSB := filepath.Join(root, "ArenaUSB")
	home := filepath.Join(root, "home")
	camPath := filepath.Join(root, "web", "static", "image.jpg")

	// 1. team corner image on the arena USB has the highest priority
	team := "Brain18"
	testJPEG(t, filepath.Join(arenaUSB, team+".jpg"))
	teamCorner, _ := os.ReadFile(filepath.Join(arenaUSB, team+".jpg"))

	cfg := &config.Config{
		TeamNameFilePath: filepath.Join(home, "teamname.txt"),
		GameLogoPath:     filepath.Join(home, "game_logo.jpg"),
		ArenaUSBPath:     arenaUSB,
		UserCodePath:     filepath.Join(root, "shepherd", "usercode"),
		CameraImagePath:  camPath,
	}
	if err := os.MkdirAll(filepath.Dir(cfg.TeamNameFilePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.TeamNameFilePath, []byte(team+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := SelectStartGraphic(cfg); err != nil {
		t.Fatalf("SelectStartGraphic: %v", err)
	}

	got, err := os.ReadFile(camPath)
	if err != nil {
		t.Fatalf("camera image not written: %v", err)
	}
	if string(got) != string(teamCorner) {
		t.Error("team corner image was not selected")
	}

	// thumbnail must exist and decode as a 1280x720 JPEG
	thumb, err := os.Open("/tmp/current.jpg")
	if err != nil {
		t.Fatalf("thumbnail missing: %v", err)
	}
	defer thumb.Close()
	decoded, _, err := image.Decode(thumb)
	if err != nil {
		t.Fatalf("thumbnail decode: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != 1280 || b.Dy() != 720 {
		t.Errorf("thumbnail size %dx%d, want 1280x720", b.Dx(), b.Dy())
	}
}

func TestSelectStartGraphicFallsBackToGameLogo(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	camPath := filepath.Join(root, "web", "static", "image.jpg")

	testJPEG(t, filepath.Join(home, "game_logo.jpg"))
	logo, _ := os.ReadFile(filepath.Join(home, "game_logo.jpg"))

	cfg := &config.Config{
		TeamNameFilePath: filepath.Join(home, "teamname.txt"), // no teamname.txt
		GameLogoPath:     filepath.Join(home, "game_logo.jpg"),
		ArenaUSBPath:     filepath.Join(root, "ArenaUSB"),
		UserCodePath:     filepath.Join(root, "shepherd", "usercode"),
		CameraImagePath:  camPath,
	}

	if err := SelectStartGraphic(cfg); err != nil {
		t.Fatalf("SelectStartGraphic: %v", err)
	}

	got, err := os.ReadFile(camPath)
	if err != nil {
		t.Fatalf("camera image not written: %v", err)
	}
	if string(got) != string(logo) {
		t.Error("game logo was not used as fallback")
	}
}

func TestSelectStartGraphicNoCandidates(t *testing.T) {
	root := t.TempDir()
	cfg := &config.Config{
		TeamNameFilePath: filepath.Join(root, "teamname.txt"),
		GameLogoPath:     filepath.Join(root, "game_logo.jpg"),
		ArenaUSBPath:     filepath.Join(root, "ArenaUSB"),
		UserCodePath:     filepath.Join(root, "shepherd", "usercode"),
		CameraImagePath:  filepath.Join(root, "web", "static", "image.jpg"),
	}
	if err := SelectStartGraphic(cfg); err != nil {
		t.Fatalf("SelectStartGraphic with no candidates: %v", err)
	}
	if _, err := os.Stat(cfg.CameraImagePath); !os.IsNotExist(err) {
		t.Error("camera image should be untouched when no candidates exist")
	}
}
