// Package graphics selects the boot-time start graphic for the robot web UI.
//
// It mirrors the legacy Python shepherd behaviour: pick the team corner image
// in order of preference and write it into the camera image path so the
// livestream shows it until the first real camera frame arrives.
package graphics

import (
	"image"
	"image/jpeg"
	"log"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"

	"github.com/RoboConOxfordshire/shepherd/internal/config"
)

// SelectStartGraphic picks the start image to show in the web UI, in the same
// order of preference as the legacy shepherd:
//
//  1. the team corner image on the arena USB ("<teamname>.jpg")
//  2. an image the team uploaded to the robot (robotsrc/team_logo.jpg)
//  3. the generic corner graphic on the arena USB (Corner.jpg)
//  4. the game-specific logo on the brain (game_logo.jpg)
//
// The chosen image is written into the camera image path (so the camera
// stream shows it until the first frame) and a 1280x720 thumbnail is written
// to /tmp/current.jpg, which is what the old /livestream endpoint served.
func SelectStartGraphic(cfg *config.Config) error {
	var candidates []string

	if data, err := os.ReadFile(cfg.TeamNameFilePath); err == nil {
		teamname := strings.ReplaceAll(strings.TrimSpace(string(data)), "\n", "")
		if teamname != "" {
			candidates = append(candidates, filepath.Join(cfg.ArenaUSBPath, teamname+".jpg"))
		}
	}
	candidates = append(candidates,
		filepath.Join(filepath.Dir(cfg.UserCodePath), "robotsrc", "team_logo.jpg"),
		filepath.Join(cfg.ArenaUSBPath, "Corner.jpg"),
		cfg.GameLogoPath,
	)

	for _, candidate := range candidates {
		raw, err := os.ReadFile(candidate)
		if err != nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(cfg.CameraImagePath), 0755); err != nil {
			log.Printf("start graphic: mkdir %s: %v", filepath.Dir(cfg.CameraImagePath), err)
		}
		if err := os.WriteFile(cfg.CameraImagePath, raw, 0644); err != nil {
			log.Printf("start graphic: write %s: %v", cfg.CameraImagePath, err)
		} else {
			log.Printf("start graphic: %s -> %s", candidate, cfg.CameraImagePath)
		}
		if err := thumbnail(candidate, "/tmp/current.jpg", 1280, 720); err != nil {
			log.Printf("start graphic: thumbnail: %v", err)
		}
		return nil
	}

	log.Printf("start graphic: none of %v present, keeping existing camera image", candidates)
	return nil
}

// thumbnail decodes src, scales it to width x height and re-encodes it as a
// JPEG at dst. Scaling is a plain stretch, matching the legacy
// PIL Image.resize((1280, 720)) call.
func thumbnail(src, dst string, width, height int) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return err
	}

	out := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(out, out.Bounds(), img, img.Bounds(), draw.Over, nil)

	of, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer of.Close()

	return jpeg.Encode(of, out, &jpeg.Options{Quality: 90})
}
