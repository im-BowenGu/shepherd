package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	body := `
port: 8080
camera_ipc: udp
camera_shm_name: /my_shm
camera_udp_addr: 127.0.0.1:9999
round_length: 90s
reap_grace_period: 2s
start_button_pin: 21
image_width: 640
image_height: 480
`
	if err := os.WriteFile(path, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SHEPHERD_CONFIG", path)
	// Make sure no ambient environment leaks into the test.
	t.Setenv("SHEPHERD_PORT", "")
	t.Setenv("SHEPHERD_ROUND_LENGTH", "")
	t.Setenv("SHEPHERD_CAMERA_IPC", "")

	cfg := Load()

	if cfg.Port != 8080 {
		t.Errorf("port: got %d, want 8080", cfg.Port)
	}
	if cfg.CameraIPC != "udp" {
		t.Errorf("camera_ipc: got %q, want %q", cfg.CameraIPC, "udp")
	}
	if cfg.CameraSHMName != "/my_shm" {
		t.Errorf("camera_shm_name: got %q", cfg.CameraSHMName)
	}
	if cfg.CameraUDPAddr != "127.0.0.1:9999" {
		t.Errorf("camera_udp_addr: got %q", cfg.CameraUDPAddr)
	}
	if cfg.RoundLength != 90*time.Second {
		t.Errorf("round_length: got %v", cfg.RoundLength)
	}
	if cfg.ReapGracePeriod != 2*time.Second {
		t.Errorf("reap_grace_period: got %v", cfg.ReapGracePeriod)
	}
	if cfg.StartButtonPin != 21 {
		t.Errorf("start_button_pin: got %d", cfg.StartButtonPin)
	}
	if cfg.ImageWidth != 640 || cfg.ImageHeight != 480 {
		t.Errorf("image size: got %vx%v", cfg.ImageWidth, cfg.ImageHeight)
	}
}

func TestEnvOverridesYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8080\ncamera_ipc: shm\n"), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SHEPHERD_CONFIG", path)
	t.Setenv("SHEPHERD_PORT", "9999")
	t.Setenv("SHEPHERD_CAMERA_IPC", "file")

	cfg := Load()
	if cfg.Port != 9999 {
		t.Errorf("port: env should win, got %d", cfg.Port)
	}
	if cfg.CameraIPC != "file" {
		t.Errorf("camera_ipc: env should win, got %q", cfg.CameraIPC)
	}
}

func TestDefaultsWhenNoFile(t *testing.T) {
	t.Setenv("SHEPHERD_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	t.Setenv("SHEPHERD_PORT", "")

	cfg := Load()
	if cfg.CameraIPC != "shm" {
		t.Errorf("camera_ipc default: got %q, want shm", cfg.CameraIPC)
	}
	if cfg.CameraSHMName != "/shepherd_cam" {
		t.Errorf("camera_shm_name default: got %q", cfg.CameraSHMName)
	}
	if cfg.Port != 80 {
		t.Errorf("port default: got %d, want 80", cfg.Port)
	}
}
