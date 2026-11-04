package run

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/RoboConOxfordshire/shepherd/internal/game"
	"github.com/RoboConOxfordshire/shepherd/internal/hardware"
)

type Manager struct {
	mu sync.Mutex

	cfg     Config
	state   State
	zone    string
	arena   string
	mode    Mode
	cmd     *exec.Cmd
	cmdWait chan error

	reaperTimer *time.Timer
	reapTime    time.Time
	startTime   time.Time
	fifoDir     string
	fifoPath    string
	outputFile  *os.File
}

type Config struct {
	UserCodeEntrypoint string
	UserCodePath       string
	RobotLibPath       string
	OutputFilePath     string
	RoundLength        time.Duration
	ReapGracePeriod    time.Duration
	Game               game.Config
}

func NewManager(cfg Config) (*Manager, error) {
	if cfg.Game.Name == "" {
		cfg.Game = game.DefaultConfig()
	}
	if cfg.RoundLength <= 0 {
		cfg.RoundLength = cfg.Game.RoundLength()
	}
	m := &Manager{
		cfg:   cfg,
		state: StateReady,
	}

	if err := m.createFIFO(); err != nil {
		return nil, fmt.Errorf("create fifo: %w", err)
	}

	return m, nil
}

func (m *Manager) createFIFO() error {
	dir, err := os.MkdirTemp("", "shepherd-fifo-")
	if err != nil {
		return err
	}
	m.fifoDir = dir
	m.fifoPath = filepath.Join(dir, "start.fifo")
	return syscall.Mkfifo(m.fifoPath, 0666)
}

func (m *Manager) StartUserCode() error {
	m.mu.Lock()
	if m.state == StateRunning {
		m.mu.Unlock()
		return fmt.Errorf("code is already running")
	}
	if m.cmdWait != nil {
		select {
		case <-m.cmdWait:
		default:
		}
	}
	m.mu.Unlock()

	entrypoint := filepath.Join(m.cfg.UserCodePath, m.cfg.UserCodeEntrypoint)

	if err := os.MkdirAll(filepath.Dir(m.cfg.OutputFilePath), 0755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}

	f, err := os.Create(m.cfg.OutputFilePath)
	if err != nil {
		return fmt.Errorf("create output file: %w", err)
	}

	cmd := exec.Command("python3", "-u", entrypoint, "--startfifo", m.fifoPath)
	cmd.Dir = m.cfg.UserCodePath
	cmd.Stdout = f
	cmd.Stderr = f
	cmd.Env = append(os.Environ(),
		"PYTHONPATH="+m.cfg.RobotLibPath,
	)

	if err := cmd.Start(); err != nil {
		f.Close()
		return fmt.Errorf("start user code: %w", err)
	}

	cmdWait := make(chan error, 1)
	go m.waitForExit(cmd, cmdWait)

	m.mu.Lock()
	m.cmd = cmd
	m.cmdWait = cmdWait
	m.outputFile = f
	m.state = StateReady
	m.mu.Unlock()

	return nil
}

func (m *Manager) waitForExit(cmd *exec.Cmd, cmdWait chan<- error) {
	err := cmd.Wait()
	cmdWait <- err

	m.mu.Lock()
	if err != nil {
		log.Printf("user code exited: %v", err)
	}
	if m.state == StateRunning {
		m.state = StatePostRun
	}
	m.mu.Unlock()
}

func (m *Manager) SendStartSignal(zone, arena string, mode Mode) error {
	m.mu.Lock()
	m.zone = zone
	m.arena = arena
	m.mode = mode
	m.state = StateRunning
	m.mu.Unlock()

	team := m.cfg.Game.TeamForZone(zone)
	data := map[string]any{
		"mode":       string(mode),
		"zone":       zone,
		"team":       string(team),
		"team_index": team.Index(),
		"arena":      arena,
		"colour":     string(team),
		// The robot library can use these to know when the Snitch window is,
		// without having to poll shepherd or guess the round length.
		"round_length_seconds": m.cfg.RoundLength.Seconds(),
		"snitch_window_opens_after_seconds": m.cfg.RoundLength.Seconds() -
			float64(m.cfg.Game.SnitchDropEarliestSeconds),
		"snitch_window_closes_after_seconds": m.cfg.RoundLength.Seconds() -
			float64(m.cfg.Game.SnitchDropLatestSeconds),
	}

	raw, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshal start data: %w", err)
	}

	// Open the fifo non-blocking: a plain WriteFile on a fifo with no reader
	// (e.g. user code that never calls wait_start) would block forever and
	// hang the /run/start request. If there is no reader yet, the code either
	// already exited or never waits - record the start and carry on.
	fd, err := syscall.Open(m.fifoPath, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if err == syscall.ENXIO {
			log.Printf("start signal: no fifo reader (user code not waiting), zone=%s arena=%s", zone, arena)
			return nil
		}
		return fmt.Errorf("open start fifo: %w", err)
	}
	defer syscall.Close(fd)
	if _, err := syscall.Write(fd, raw); err != nil {
		if err == syscall.EAGAIN || err == syscall.ENXIO {
			log.Printf("start signal: unable to deliver (no reader), zone=%s arena=%s", zone, arena)
			return nil
		}
		return fmt.Errorf("write start fifo: %w", err)
	}
	return nil
}

// colourForZone maps the 1-based zone file on the arena USB to the team colour
// for the 2027 Quidditch game. Zone 0 is the historical "no zone" fallback and
// is treated as the red team.
func colourForZone(zone string) string {
	return string(game.TeamForZone(zone))
}

func (m *Manager) StartReaper() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.startTime = time.Now()
	m.reapTime = m.startTime.Add(m.cfg.RoundLength)
	m.reaperTimer = time.AfterFunc(m.cfg.RoundLength, func() {
		m.Reap("end of round")
	})
}

func (m *Manager) StopReaper() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.reaperTimer != nil {
		m.reaperTimer.Stop()
		m.reaperTimer = nil
	}
}

func (m *Manager) Reap(reason string) {
	m.mu.Lock()
	log.Printf("Reaping user code (%s)", reason)
	if m.state != StateRunning {
		log.Printf("Warning: state is %s, not running", m.state)
		m.mu.Unlock()
		return
	}
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return
	}
	cmdWait := m.cmdWait
	m.mu.Unlock()

	if err := m.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		log.Printf("SIGTERM failed: %v", err)
	}

	select {
	case <-cmdWait:
	case <-time.After(m.cfg.ReapGracePeriod):
		log.Println("Butchering user code")
		if err := m.cmd.Process.Signal(syscall.SIGKILL); err != nil {
			log.Printf("SIGKILL failed: %v", err)
		}
		<-cmdWait
	}

	m.mu.Lock()
	if m.outputFile != nil {
		m.outputFile.WriteString("\n==== END OF ROUND ====\n\n")
		m.outputFile.Close()
		m.outputFile = nil
	}
	m.state = StatePostRun
	m.mu.Unlock()

	log.Println("Done reaping user code")
}

func (m *Manager) Stop() {
	m.StopReaper()
	m.Reap("manual stop")
}

func (m *Manager) Reset() {
	m.mu.Lock()
	m.state = StateReady
	m.zone = ""
	m.arena = ""
	m.mode = ""
	m.startTime = time.Time{}
	m.reapTime = time.Time{}

	if m.outputFile != nil {
		m.outputFile.Close()
		m.outputFile = nil
	}

	if m.fifoDir != "" {
		os.RemoveAll(m.fifoDir)
		m.fifoDir = ""
		m.fifoPath = ""
	}
	m.mu.Unlock()

	m.createFIFO()

	if err := hardware.Reset(1); err != nil {
		if !hardware.BoardPresent(1) {
			// Nothing on the bus: the Python fallback would fail the same way,
			// but only after a cold cv2/libLLVM import (seconds) on the upload
			// path, where every millisecond is felt.
			log.Printf("Skipping hardware reset, no board on I2C bus 1: %v", err)
		} else {
			log.Printf("Go hardware reset failed, falling back to Python: %v", err)
			cmd := exec.Command("python3", "-c", "import robot.reset; robot.reset.reset()")
			cmd.Env = append(os.Environ(), "PYTHONPATH="+m.cfg.RobotLibPath)
			if out, err := cmd.CombinedOutput(); err != nil {
				log.Printf("robot.reset (Python fallback) also failed: %v\n%s", err, out)
			}
		}
	}
}

func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.state == StateRunning && m.cmd != nil && m.cmd.ProcessState != nil && m.cmd.ProcessState.Exited() {
		m.state = StatePostRun
	}
	return m.state
}

func (m *Manager) TimeLeft() *time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.reapTime.IsZero() {
		return nil
	}
	left := time.Until(m.reapTime)
	if left <= 0 {
		return nil
	}
	return &left
}

// Timing returns the round clock and Snitch-window state for the current
// round.  It returns nil before a competition round has started.
func (m *Manager) Timing() *game.Timing {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.startTime.IsZero() {
		return nil
	}
	timing := m.cfg.Game.TimingAt(time.Since(m.startTime))
	return &timing
}

// Team returns the team for the current round.
func (m *Manager) Team() game.Team {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg.Game.TeamForZone(m.zone)
}

func (m *Manager) Zone() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.zone
}

func (m *Manager) Arena() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.arena
}

func (m *Manager) Mode() Mode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.mode
}

func (m *Manager) Cleanup() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.reaperTimer != nil {
		m.reaperTimer.Stop()
	}
	if m.cmd != nil && m.cmd.Process != nil {
		m.cmd.Process.Kill()
	}
	if m.cmdWait != nil {
		select {
		case <-m.cmdWait:
		default:
		}
	}
	if m.outputFile != nil {
		m.outputFile.Close()
	}
	if m.fifoDir != "" {
		os.RemoveAll(m.fifoDir)
	}
}
