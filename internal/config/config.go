package config

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Port               int           `yaml:"port"`
	UserCodePath       string        `yaml:"user_code_path"`
	UserCodeEntrypoint string        `yaml:"user_code_entrypoint"`
	RobotLibPath       string        `yaml:"robot_lib_path"`
	OutputFilePath     string        `yaml:"output_file_path"`
	CameraImagePath    string        `yaml:"camera_image_path"`
	CameraIPC          string        `yaml:"camera_ipc"`
	CameraSHMName      string        `yaml:"camera_shm_name"`
	CameraUDPAddr      string        `yaml:"camera_udp_addr"`
	ArenaUSBPath       string        `yaml:"arena_usb_path"`
	TeamNameFilePath   string        `yaml:"team_name_file_path"`
	GameLogoPath       string        `yaml:"game_logo_path"`
	RoundLength        time.Duration `yaml:"round_length"`
	ReapGracePeriod    time.Duration `yaml:"reap_grace_period"`
	StartButtonPin     int           `yaml:"start_button_pin"`
	ImageWidth         float64       `yaml:"image_width"`
	ImageHeight        float64       `yaml:"image_height"`
	GameConfigPath     string        `yaml:"game_config_path"`
}

type yamlConfig struct {
	Port               int    `yaml:"port"`
	UserCodePath       string `yaml:"user_code_path"`
	UserCodeEntrypoint string `yaml:"user_code_entrypoint"`
	RobotLibPath       string `yaml:"robot_lib_path"`
	OutputFilePath     string `yaml:"output_file_path"`
	CameraImagePath    string `yaml:"camera_image_path"`
	CameraIPC          string `yaml:"camera_ipc"`
	CameraSHMName      string `yaml:"camera_shm_name"`
	CameraUDPAddr      string `yaml:"camera_udp_addr"`
	ArenaUSBPath       string `yaml:"arena_usb_path"`
	TeamNameFilePath   string `yaml:"team_name_file_path"`
	GameLogoPath       string `yaml:"game_logo_path"`
	RoundLength        string `yaml:"round_length"`
	ReapGracePeriod    string `yaml:"reap_grace_period"`
	StartButtonPin     int    `yaml:"start_button_pin"`
	ImageWidth         int    `yaml:"image_width"`
	ImageHeight        int    `yaml:"image_height"`
	GameConfigPath     string `yaml:"game_config_path"`
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvDur(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}

func defaultConfig() *Config {
	home := getEnv("HOME", "/home/pi")
	wd, _ := os.Getwd()

	return &Config{
		Port:               80,
		UserCodePath:       filepath.Join(wd, "usercode"),
		UserCodeEntrypoint: "main.py",
		RobotLibPath:       filepath.Join(home, "robot"),
		OutputFilePath:     "/media/RobotUSB/logs.txt",
		CameraImagePath:    filepath.Join(wd, "shepherd", "static", "image.jpg"),
		CameraIPC:          "shm",
		CameraSHMName:      "/shepherd_cam",
		CameraUDPAddr:      "127.0.0.1:9001",
		ArenaUSBPath:       "/media/ArenaUSB",
		TeamNameFilePath:   filepath.Join(home, "teamname.txt"),
		GameLogoPath:       filepath.Join(home, "game_logo.jpg"),
		RoundLength:        180 * time.Second,
		ReapGracePeriod:    5 * time.Second,
		StartButtonPin:     26,
		ImageWidth:         800,
		ImageHeight:        600,
		GameConfigPath:     "",
	}
}

func Load() *Config {
	cfg := defaultConfig()

	// YAML config file (path from env or default locations)
	yamlPath := getEnv("SHEPHERD_CONFIG", "/etc/shepherd/config.yaml")
	if _, err := os.Stat(yamlPath); err != nil {
		// Try fallback locations
		for _, p := range []string{
			"/etc/shepherd.yaml",
			"/opt/shepherd/config.yaml",
		} {
			if _, err := os.Stat(p); err == nil {
				yamlPath = p
				break
			}
		}
	}

	if data, err := os.ReadFile(yamlPath); err == nil {
		var yc yamlConfig
		if err := yaml.Unmarshal(data, &yc); err == nil {
			if yc.Port != 0 {
				cfg.Port = yc.Port
			}
			if yc.UserCodePath != "" {
				cfg.UserCodePath = yc.UserCodePath
			}
			if yc.UserCodeEntrypoint != "" {
				cfg.UserCodeEntrypoint = yc.UserCodeEntrypoint
			}
			if yc.RobotLibPath != "" {
				cfg.RobotLibPath = yc.RobotLibPath
			}
			if yc.OutputFilePath != "" {
				cfg.OutputFilePath = yc.OutputFilePath
			}
			if yc.CameraImagePath != "" {
				cfg.CameraImagePath = yc.CameraImagePath
			}
			if yc.CameraIPC != "" {
				cfg.CameraIPC = yc.CameraIPC
			}
			if yc.CameraSHMName != "" {
				cfg.CameraSHMName = yc.CameraSHMName
			}
			if yc.CameraUDPAddr != "" {
				cfg.CameraUDPAddr = yc.CameraUDPAddr
			}
			if yc.ArenaUSBPath != "" {
				cfg.ArenaUSBPath = yc.ArenaUSBPath
			}
			if yc.TeamNameFilePath != "" {
				cfg.TeamNameFilePath = yc.TeamNameFilePath
			}
			if yc.GameLogoPath != "" {
				cfg.GameLogoPath = yc.GameLogoPath
			}
			if yc.RoundLength != "" {
				if d, err := time.ParseDuration(yc.RoundLength); err == nil {
					cfg.RoundLength = d
				}
			}
			if yc.ReapGracePeriod != "" {
				if d, err := time.ParseDuration(yc.ReapGracePeriod); err == nil {
					cfg.ReapGracePeriod = d
				}
			}
			if yc.StartButtonPin != 0 {
				cfg.StartButtonPin = yc.StartButtonPin
			}
			if yc.ImageWidth != 0 {
				cfg.ImageWidth = float64(yc.ImageWidth)
			}
			if yc.ImageHeight != 0 {
				cfg.ImageHeight = float64(yc.ImageHeight)
			}
			if yc.GameConfigPath != "" {
				cfg.GameConfigPath = yc.GameConfigPath
			}
		}
	}

	// Environment variable overrides (backward compatibility)
	cfg.Port = getEnvInt("SHEPHERD_PORT", cfg.Port)
	cfg.UserCodePath = getEnv("SHEPHERD_USER_CODE_PATH", cfg.UserCodePath)
	cfg.RobotLibPath = getEnv("SHEPHERD_ROBOT_LIB_PATH", cfg.RobotLibPath)
	cfg.OutputFilePath = getEnv("SHEPHERD_OUTPUT_FILE", cfg.OutputFilePath)
	cfg.CameraImagePath = getEnv("SHEPHERD_CAMERA_IMAGE", cfg.CameraImagePath)
	cfg.CameraIPC = getEnv("SHEPHERD_CAMERA_IPC", cfg.CameraIPC)
	cfg.CameraSHMName = getEnv("SHEPHERD_CAMERA_SHM", cfg.CameraSHMName)
	cfg.CameraUDPAddr = getEnv("SHEPHERD_CAMERA_UDP", cfg.CameraUDPAddr)
	cfg.ArenaUSBPath = getEnv("SHEPHERD_ARENA_USB_PATH", cfg.ArenaUSBPath)
	cfg.TeamNameFilePath = getEnv("SHEPHERD_TEAM_NAME_FILE", cfg.TeamNameFilePath)
	cfg.GameLogoPath = getEnv("SHEPHERD_GAME_LOGO_PATH", cfg.GameLogoPath)
	cfg.RoundLength = getEnvDur("SHEPHERD_ROUND_LENGTH", cfg.RoundLength)
	cfg.ReapGracePeriod = getEnvDur("SHEPHERD_REAP_GRACE", cfg.ReapGracePeriod)
	cfg.StartButtonPin = getEnvInt("SHEPHERD_START_BUTTON_PIN", cfg.StartButtonPin)
	cfg.ImageWidth = float64(getEnvInt("SHEPHERD_IMAGE_WIDTH", int(cfg.ImageWidth)))
	cfg.ImageHeight = float64(getEnvInt("SHEPHERD_IMAGE_HEIGHT", int(cfg.ImageHeight)))
	cfg.GameConfigPath = getEnv("SHEPHERD_GAME_CONFIG", cfg.GameConfigPath)

	return cfg
}
