// Package sysinfo reports what shepherd is running on and what it can see of
// the hardware. The legacy brain printed a startup banner and a hardware
// status report on every boot; this reproduces that for the Go brain and
// exposes the same data over HTTP for the web UI.
package sysinfo

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/RoboConOxfordshire/shepherd/internal/hardware"
)

// Version details, stamped at build time via -ldflags -X.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildDate = "unknown"
)

// Info is the structured form of the report, served by /api/sysinfo.
type Info struct {
	Version   string            `json:"version"`
	Commit    string            `json:"commit"`
	BuildDate string            `json:"build_date"`
	Go        string            `json:"go"`
	Arch      string            `json:"arch"`
	Hostname  string            `json:"hostname"`
	Kernel    string            `json:"kernel"`
	Started   string            `json:"started"`
	Uptime    string            `json:"uptime"`
	Memory    string            `json:"memory"`
	Swap      string            `json:"swap"`
	DiskRoot  string            `json:"disk_root"`
	Paths     map[string]string `json:"paths"`
	Hardware  map[string]string `json:"hardware"`
	Board     hardware.Status   `json:"board"`
	Warnings  []string          `json:"warnings"`
}

var startedAt = time.Now()

func readTrimmed(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func memInfo() (total, available string) {
	for _, line := range strings.Split(readTrimmed("/proc/meminfo"), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "MemTotal:":
			total = fields[1] + " kB"
		case "MemAvailable:":
			available = fields[1] + " kB"
		}
	}
	if total != "" {
		total = fmt.Sprintf("%s (available %s)", total, available)
	}
	return
}

func rootDisk() string {
	var st syscall.Statfs_t
	if err := syscall.Statfs("/", &st); err != nil {
		return "unknown"
	}
	used := (st.Blocks - st.Bfree) * uint64(st.Bsize)
	total := st.Blocks * uint64(st.Bsize)
	return fmt.Sprintf("%.1f%% used (%s free)",
		float64(used)/float64(total)*100,
		humanBytes(uint64(st.Bavail)*uint64(st.Bsize)))
}

func humanBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

func glob(path string) []string {
	matches, err := filepath.Glob(path)
	if err != nil {
		return nil
	}
	sort.Strings(matches)
	return matches
}

// Collect gathers the full system report, including the board bring-up state.
func Collect(board hardware.Status) Info {
	kernel := readTrimmed("/proc/version")
	if kernel != "" {
		fields := strings.SplitN(kernel, " ", 3)
		if len(fields) >= 3 {
			kernel = fields[2]
		}
	}

	mem, _ := memInfo()
	hostname, _ := os.Hostname()

	info := Info{
		Version:   Version,
		Commit:    Commit,
		BuildDate: BuildDate,
		Go:        runtime.Version(),
		Arch:      runtime.GOOS + "/" + runtime.GOARCH,
		Hostname:  hostname,
		Kernel:    kernel,
		Started:   startedAt.Format(time.RFC3339),
		Uptime:    time.Since(startedAt).Truncate(time.Second).String(),
		Memory:    mem,
		DiskRoot:  rootDisk(),
		Paths: map[string]string{
			"usercode":     "/opt/shepherd/usercode",
			"robot_lib":    "/opt/robot",
			"camera_image": "/home/pi/shepherd/shepherd/static/image.jpg",
			"output":       "/media/RobotUSB/logs.txt",
		},
		Hardware: map[string]string{},
		Board:    board,
		Warnings: []string{},
	}

	// Swap (zram) - the stage1 script sizes it, so report what we got.
	if st, err := os.Stat("/sys/block/zram0/disksize"); err == nil {
		if info.Swap = strings.TrimSpace(readTrimmed(st.Name())); info.Swap != "" {
			info.Swap = humanBytes(uint64(mustAtoi(info.Swap)))
		}
	}

	// I2C buses and whether the motor board answers on bus 1.
	buses := glob("/dev/i2c-*")
	if len(buses) == 0 {
		info.Warnings = append(info.Warnings,
			"no I2C buses present - dtparam=i2c_arm=on missing or i2c-bcm2835 not loaded")
	}
	info.Hardware["i2c_buses"] = strings.Join(baseNames(buses), " ")

	// Board: the level shifter must be enabled before the MCU will talk, and
	// without it every I2C transaction fails with EREMOTEIO.
	switch {
	case !board.LevelShifter:
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"level shifter on GPIO%d not enabled - the board cannot answer on I2C",
			board.LevelShifterPin))
		info.Hardware["board"] = "level shifter disabled"
	case board.BoardPresent:
		info.Hardware["board"] = fmt.Sprintf("present, firmware v%d", board.BoardVersion)
	default:
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"no board answering on I2C: %s", board.LastError))
		info.Hardware["board"] = "no answer"
	}
	if board.KeepaliveFails > 0 {
		info.Warnings = append(info.Warnings, fmt.Sprintf(
			"board keepalive failed %d time(s) - the MCU may have dropped the power rails",
			board.KeepaliveFails))
	}

	// Camera: media devices are the only reliable indicator; the robot lib owns
	// the sensor and libcamera cannot be probed from here without grabbing it.
	media := glob("/dev/media*")
	video := glob("/dev/video*")
	if len(media) == 0 {
		info.Warnings = append(info.Warnings,
			"no /dev/media* nodes - CSI camera driver not bound, robot.see() will find nothing")
	}
	info.Hardware["media_devices"] = strings.Join(baseNames(media), " ")
	info.Hardware["video_devices"] = strings.Join(baseNames(video), " ")

	info.Hardware["gpiochips"] = strings.Join(baseNames(glob("/dev/gpiochip*")), " ")
	if len(info.Hardware["gpiochips"]) == 0 {
		info.Warnings = append(info.Warnings, "no /dev/gpiochip* - start button and level shifter will not work")
	}

	// Network interfaces, so a mis-named AP interface is obvious at boot.
	ifaces := glob("/sys/class/net/*")
	info.Hardware["interfaces"] = strings.Join(baseNames(ifaces), " ")

	return info
}

func mustAtoi(s string) int64 {
	var v int64
	_, _ = fmt.Sscanf(s, "%d", &v)
	return v
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, filepath.Base(p))
	}
	return out
}

// Render formats the report the way the legacy brain printed it.
func (i Info) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== shepherd %s (%s, built %s) ===\n", i.Version, i.Commit[:min(len(i.Commit), 8)], i.BuildDate)
	fmt.Fprintf(&b, "host      : %s\n", i.Hostname)
	fmt.Fprintf(&b, "platform  : %s, kernel %s, go %s\n", i.Arch, i.Kernel, i.Go)
	fmt.Fprintf(&b, "started   : %s (uptime %s)\n", i.Started, i.Uptime)
	fmt.Fprintf(&b, "memory    : %s\n", i.Memory)
	fmt.Fprintf(&b, "swap      : %s\n", i.Swap)
	fmt.Fprintf(&b, "root disk : %s\n", i.DiskRoot)
	fmt.Fprintf(&b, "------HARDWARE REPORT------\n")
	fmt.Fprintf(&b, "level shifter : GPIO%d %s\n", i.Board.LevelShifterPin, enabledText(i.Board.LevelShifter))
	fmt.Fprintf(&b, "start button  : GPIO%d %s\n", i.Board.StartButtonPin, buttonText(i.Board.StartButtonOK))
	fmt.Fprintf(&b, "Robocon Board : %s\n", boardText(i.Board))
	fmt.Fprintf(&b, "board keepalive: %s\n", keepaliveText(i.Board.KeepaliveFails))
	for _, key := range sortedKeys(i.Hardware) {
		fmt.Fprintf(&b, "%-10s: %s\n", key, i.Hardware[key])
	}
	fmt.Fprintf(&b, "---------------------------\n")
	for _, key := range sortedKeys(i.Paths) {
		fmt.Fprintf(&b, "%-10s: %s\n", key, i.Paths[key])
	}
	for _, w := range i.Warnings {
		fmt.Fprintf(&b, "WARNING   : %s\n", w)
	}
	b.WriteString("=== end of report ===\n")
	return b.String()
}

func enabledText(on bool) string {
	if on {
		return "enabled (board reachable)"
	}
	return "DISABLED"
}

func buttonText(ok bool) string {
	if ok {
		return "readable"
	}
	return "unavailable"
}

func boardText(b hardware.Status) string {
	switch {
	case b.BoardPresent && b.Initialised:
		return fmt.Sprintf("Yes (v%d, brain handshake sent)", b.BoardVersion)
	case b.BoardPresent:
		return fmt.Sprintf("Yes (v%d, handshake not sent)", b.BoardVersion)
	case b.LastError != "":
		return fmt.Sprintf("No answer (%s)", b.LastError)
	default:
		return "not probed yet"
	}
}

func keepaliveText(fails int) string {
	if fails == 0 {
		return "healthy"
	}
	return fmt.Sprintf("%d consecutive failures", fails)
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
