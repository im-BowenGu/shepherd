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

	// runSeq increments on every stop/reset so a pending async start-signal
	// delivery can detect that the round it belonged to is gone.
	runSeq      uint64
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
	// A previous child should have been reaped (cmdWait nil'd) before we get
	// here. If one is still tracked, refuse: two live user-code processes
	// would both try to acquire the camera.
	if m.cmdWait != nil {
		m.mu.Unlock()
		return fmt.Errorf("previous user code is still running")
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
	// Put the child in its own process group so a kill can take down the whole
	// tree. If user code (or a library it pulls in) forks, killing only the
	// direct child would orphan the grandchild, which keeps /dev/media* held
	// and makes the next run fail to acquire the camera.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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
	m.runSeq++
	seq := m.runSeq
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

	// The start signal is delivered asynchronously with retries. User code can
	// take several seconds to reach its blocking open() of the start fifo
	// (importing cv2, libcamera init), and a single non-blocking open at that
	// moment would drop the signal silently - the round then shows as running
	// while user code blocks in wait_start() forever. Retry until the reader
	// appears, so a Start press always takes effect.
	go m.deliverStartSignal(raw, seq, zone, arena)
	return nil
}

// deliverStartSignal writes the start payload to the fifo, retrying until the
// user-code process opens its end. Aborts if the run was superseded (stop or
// reset bumped runSeq) so a stale signal can never start freshly uploaded
// code. Marks the round running only once the signal is actually delivered,
// so /run/status reflects real code-start rather than the button press.
func (m *Manager) deliverStartSignal(raw []byte, seq uint64, zone, arena string) {
	defer func() {
		m.mu.Lock()
		if m.runSeq == seq {
			m.state = StateRunning
			log.Printf("start signal: round running (zone=%s arena=%s)", zone, arena)
		}
		m.mu.Unlock()
	}()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m.mu.Lock()
		stale := m.runSeq != seq
		m.mu.Unlock()
		if stale {
			log.Printf("start signal: aborted, round superseded (zone=%s arena=%s)", zone, arena)
			return
		}
		fd, err := syscall.Open(m.fifoPath, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == nil {
			n, werr := syscall.Write(fd, raw)
			syscall.Close(fd)
			if werr == nil && n == len(raw) {
				log.Printf("start signal: delivered (zone=%s arena=%s)", zone, arena)
				return
			}
			if werr != nil && werr != syscall.EAGAIN {
				log.Printf("start signal: write fifo: %v", werr)
				return
			}
		} else if err != syscall.ENXIO {
			log.Printf("start signal: open fifo: %v", err)
			return
		}
		if time.Now().After(deadline) {
			log.Printf("start signal: no fifo reader within 30s (user code never waited), zone=%s arena=%s", zone, arena)
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
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
	// Kill the child whenever one exists, regardless of run state. A child is
	// live in both StateReady (loaded, waiting for the start button) and
	// StateRunning (round in progress). Refusing to reap in StateReady leaves
	// the process holding the libcamera pipeline, so the next upload/start
	// collides with it ("Pipeline handler in use by another process").
	if m.cmd == nil || m.cmd.Process == nil {
		m.mu.Unlock()
		return
	}
	cmdWait := m.cmdWait
	m.mu.Unlock()

	// Signal the whole process group (negative pid) so any children the user
	// code spawned are killed too, not just the direct python3 process.
	signalGroup(m.cmd.Process.Pid, syscall.SIGTERM)

	if cmdWait != nil {
		select {
		case <-cmdWait:
		case <-time.After(m.cfg.ReapGracePeriod):
			log.Println("Butchering user code")
			signalGroup(m.cmd.Process.Pid, syscall.SIGKILL)
			<-cmdWait
		}
	}

	m.mu.Lock()
	if m.outputFile != nil {
		m.outputFile.WriteString("\n==== END OF ROUND ====\n\n")
		m.outputFile.Close()
		m.outputFile = nil
	}
	// The child is dead: drop the handles so a later Reap()/StartUserCode()
	// isn't fooled into thinking one is still alive.
	// Give libcamera a moment to release the camera device after the child
	// dies; a new round starting immediately otherwise finds the pipeline
	// still held by the dying process and crashes with "Pipeline handler in
	// use by another process".
	time.Sleep(300 * time.Millisecond)
	m.cmd = nil
	m.cmdWait = nil
	m.state = StatePostRun
	m.mu.Unlock()

	log.Println("Done reaping user code")
}

func (m *Manager) Stop() {
	m.StopReaper()
	m.Reap("manual stop")
}

// signalGroup sends sig to the process group led by pid. Falls back to a
// direct signal if the group send fails (e.g. the child already exited).
func signalGroup(pid int, sig syscall.Signal) {
	if err := syscall.Kill(-pid, sig); err != nil {
		syscall.Kill(pid, sig)
	}
}

// EnsureUserCode starts the user-code process only if one isn't already
// running. The process is normally launched at boot and idles waiting for the
// start signal, so a run start must not spawn a second copy (which would fight
// over the libcamera pipeline).
func (m *Manager) EnsureUserCode() error {
	m.mu.Lock()
	live := m.cmd != nil && m.cmdWait != nil
	m.mu.Unlock()
	if live {
		return nil
	}
	return m.StartUserCode()
}

func (m *Manager) Reset() {
	m.mu.Lock()
	m.state = StateReady
	m.zone = ""
	m.arena = ""
	m.mode = ""
	m.startTime = time.Time{}
	m.reapTime = time.Time{}
	m.runSeq++ // invalidate any in-flight async start-signal delivery

	if m.outputFile != nil {
		m.outputFile.Close()
		m.outputFile = nil
	}

	if m.fifoDir != "" {
		os.RemoveAll(m.fifoDir)
		m.fifoDir = ""
		m.fifoPath = ""
	}
	// Clear the handle to the previous child. Stop()/Reap() has already killed
	// and waited on it; keeping the stale cmd/cmdWait around makes the next
	// Reap() signal a dead process and blocks StartUserCode() forever.
	m.cmd = nil
	m.cmdWait = nil
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
		signalGroup(m.cmd.Process.Pid, syscall.SIGKILL)
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
