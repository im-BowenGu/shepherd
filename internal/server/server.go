package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/RoboConOxfordshire/shepherd/internal/config"
	"github.com/RoboConOxfordshire/shepherd/internal/editor"
	"github.com/RoboConOxfordshire/shepherd/internal/game"
	"github.com/RoboConOxfordshire/shepherd/internal/gpio"
	"github.com/RoboConOxfordshire/shepherd/internal/graphics"
	"github.com/RoboConOxfordshire/shepherd/internal/hardware"
	"github.com/RoboConOxfordshire/shepherd/internal/run"
	"github.com/RoboConOxfordshire/shepherd/internal/sysinfo"
	"github.com/RoboConOxfordshire/shepherd/internal/upload"
	"github.com/RoboConOxfordshire/shepherd/internal/ws"
)

// hardwareBus is the I2C bus the RoboCon board is wired to (the Pi's
// user/auxiliary bus, not the camera's).
const hardwareBus = 1

type Server struct {
	cfg       *config.Config
	mux       http.Handler
	runMgr    *run.Manager
	editorH   *editor.Handler
	uploadH   *upload.UploadHandler
	wsHub     *ws.Hub
	camStream *ws.CameraStreamer
	logStream *ws.LogStreamer
	pylsProxy *ws.PylsProxy
	gpioBtn   *gpio.Button
	staticFS  fs.FS
	upgrader  websocket.Upgrader
	gameCfg   game.Config
}

func New(cfg *config.Config, staticFS fs.FS) (*Server, error) {
	gameCfg, err := game.Load(cfg.GameConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load game config: %w", err)
	}

	runCfg := run.Config{
		UserCodeEntrypoint: cfg.UserCodeEntrypoint,
		UserCodePath:       cfg.UserCodePath,
		RobotLibPath:       cfg.RobotLibPath,
		OutputFilePath:     cfg.OutputFilePath,
		RoundLength:        cfg.RoundLength,
		ReapGracePeriod:    cfg.ReapGracePeriod,
		Game:               gameCfg,
	}

	runMgr, err := run.NewManager(runCfg)
	if err != nil {
		return nil, fmt.Errorf("run manager: %w", err)
	}

	wsHub := ws.NewHub()

	s := &Server{
		cfg:      cfg,
		runMgr:   runMgr,
		wsHub:    wsHub,
		staticFS: staticFS,
		upgrader: websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }},
		gameCfg:  gameCfg,
	}
	// The usercode dir must exist for the upload swap and the run manager.
	// Recreate it at startup so a broken/stale flash heals itself.
	if err := os.MkdirAll(cfg.UserCodePath, 0755); err != nil {
		log.Printf("create usercode dir: %v", err)
	}
	s.editorH = editor.NewHandler(filepath.Join("robotsrc"))
	s.uploadH = upload.NewHandler(cfg.UserCodePath, cfg.UserCodeEntrypoint, s.onUpload)
	s.camStream = ws.NewCameraStreamer(wsHub, cfg.CameraImagePath, cfg.ImageWidth, cfg.ImageHeight)
	s.logStream = ws.NewLogStreamer(wsHub, cfg.OutputFilePath)
	s.pylsProxy = ws.NewPylsProxy(cfg.RobotLibPath)

	s.gpioBtn = gpio.NewButton(cfg.StartButtonPin, cfg.ArenaUSBPath, s.onStartButton)
	s.setupRoutes()

	return s, nil
}

func (s *Server) onUpload() {
	// The board may have been asleep (the MCU drops the rails if it goes
	// quiet), so make sure the level shifter is asserted before resetting.
	if st := hardware.BoardStatus(); !st.BoardPresent && st.LevelShifter {
		hardware.BringUp(hardwareBus)
	}
	s.runMgr.Stop()
	s.runMgr.Reset()
	if err := s.runMgr.StartUserCode(); err != nil {
		log.Printf("restart user code after upload: %v", err)
	}
}

func (s *Server) onStartButton(zone, arena string) {
	if s.runMgr.State() == run.StateReady {
		s.runMgr.StartUserCode()
		s.runMgr.SendStartSignal(zone, arena, run.ModeCompetition)
		s.runMgr.StartReaper()
	}
}

// announce writes a line-block of text into the program output, which is what
// the web UI shows in its log pane.
func (s *Server) announce(text string) {
	f, err := os.OpenFile(s.cfg.OutputFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		log.Printf("announce: %v", err)
		return
	}
	defer f.Close()
	if _, err := f.WriteString(text); err != nil {
		log.Printf("announce: %v", err)
	}
}

func (s *Server) Run(ctx context.Context) error {
	// Bring the board up before anything else touches it: the level shifter
	// gate (GPIO18) is what makes the PiLow answer on I2C at all, and the
	// board's MCU drops the power rails if it never sees a brain.
	boardStatus := hardware.BringUp(hardwareBus)

	// Legacy brain behaviour: a startup banner with version and hardware
	// diagnostics, so a misconfigured brain is obvious from the log alone.
	report := sysinfo.Collect(boardStatus)
	log.Print(report.Render())

	if err := graphics.SelectStartGraphic(s.cfg); err != nil {
		log.Printf("start graphic selection: %v", err)
	}

	// Match the legacy shepherd: create the output file up front so the web UI
	// poll of /run/output never 404s, and make sure the camera image path has
	// a frame to serve until the robot's vision writes one.
	if err := os.MkdirAll(filepath.Dir(s.cfg.OutputFilePath), 0755); err != nil {
		log.Printf("create output dir: %v", err)
	}
	if f, err := os.OpenFile(s.cfg.OutputFilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644); err == nil {
		f.Close()
	}
	if err := os.MkdirAll(filepath.Dir(s.cfg.CameraImagePath), 0755); err != nil {
		log.Printf("create camera dir: %v", err)
	}
	if _, err := os.Stat(s.cfg.CameraImagePath); os.IsNotExist(err) {
		if f, err := os.Create(s.cfg.CameraImagePath); err == nil {
			f.Close()
		}
	}

	// The old brain printed its startup banner and hardware report into the
	// program output, which is what the web UI shows in the log pane. Keep
	// that: write the same report there too.
	s.announce(report.Render())

	// Match the legacy shepherd: start user code as soon as the server is up,
	// so a USB stick's main.py (or uploaded code) runs and keeps the PiLow
	// MCU alive without waiting for the start button.
	if _, err := os.Stat(filepath.Join(s.cfg.UserCodePath, s.cfg.UserCodeEntrypoint)); err == nil {
		if err := s.runMgr.StartUserCode(); err != nil {
			log.Printf("start user code at boot: %v", err)
		}
	}

	s.camStream.Start()
	s.logStream.Start()
	s.gpioBtn.Start()

	httpServer := &http.Server{
		Addr:    fmt.Sprintf(":%d", s.cfg.Port),
		Handler: s.mux,
	}

	// The original web UI connects to a websocket feeder on :5001 for the
	// camera and program-output streams ([CAMERA]/[LOGS] prefixed frames,
	// mirroring the legacy helper.py daemon).
	wsMux := http.NewServeMux()
	wsMux.HandleFunc("/", s.handleWSCameraLogs)
	wsServer := &http.Server{Addr: ":5001", Handler: wsMux}
	go func() {
		log.Printf("Shepherd ws feeder listening on :5001")
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("ws feeder: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdownCtx)
		wsServer.Shutdown(shutdownCtx)
		s.runMgr.Cleanup()
	}()

	log.Printf("Shepherd listening on :%d", s.cfg.Port)
	if err := httpServer.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) setupRoutes() {
	mux := http.NewServeMux()

	// Run API
	mux.HandleFunc("GET /api/run/status", s.handleRunStatus)
	mux.HandleFunc("POST /api/run/start", s.handleRunStart)
	mux.HandleFunc("POST /api/run/stop", s.handleRunStop)
	mux.HandleFunc("GET /api/run/output", s.handleRunOutput)
	mux.HandleFunc("GET /api/run/time-left", s.handleRunTimeLeft)
	mux.HandleFunc("GET /api/run/picture", s.handleRunPicture)

	// Version and hardware diagnostics
	mux.HandleFunc("GET /api/sysinfo", s.handleSysInfo)
	mux.HandleFunc("GET /api/version", s.handleVersion)

	// 2027 Quidditch game rules, scoring, league and knockout helpers
	mux.HandleFunc("GET /api/game", s.handleGameInfo)
	mux.HandleFunc("POST /api/game/score", s.handleGameScore)
	mux.HandleFunc("POST /api/game/league", s.handleGameLeague)
	mux.HandleFunc("POST /api/game/knockout", s.handleGameKnockout)

	// Upload API
	mux.HandleFunc("GET /api/upload", s.handleUploadIndex)
	mux.HandleFunc("POST /api/upload", s.handleUploadFile)

	// Editor API
	mux.HandleFunc("GET /api/files", s.handleEditorList)
	mux.HandleFunc("GET /api/files/{filename}", s.handleEditorRead)
	mux.HandleFunc("POST /api/files/{filename}", s.handleEditorWrite)
	mux.HandleFunc("DELETE /api/files/{filename}", s.handleEditorDelete)

	// WebSocket endpoints
	mux.HandleFunc("GET /ws/camera", s.handleWSCamera)
	mux.HandleFunc("GET /ws/logs", s.handleWSLogs)
	mux.HandleFunc("GET /ws/pyls", s.handleWSPyls)

	// Legacy route aliases used by the original Vue frontend bundle
	mux.HandleFunc("GET /files/", s.handleEditorList)
	mux.HandleFunc("POST /files/save/{filename}", s.handleEditorWrite)
	mux.HandleFunc("DELETE /files/delete/{filename}", s.handleEditorDelete)
	mux.HandleFunc("GET /upload/upload", s.handleUploadIndex)
	mux.HandleFunc("POST /upload/upload", s.handleUploadFile)
	mux.HandleFunc("GET /run/output", s.handleRunOutput)
	mux.HandleFunc("POST /run/start", s.handleRunStart)
	mux.HandleFunc("POST /run/stop", s.handleRunStop)
	mux.HandleFunc("POST /api/run/reset", s.handleRunReset)

	// Static files
	mux.HandleFunc("GET /", s.handleStatic)

	s.mux = corsMiddleware(mux)
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// --- Run handlers ---

func (s *Server) handleRunStatus(w http.ResponseWriter, r *http.Request) {
	state := s.runMgr.State()
	writeJSON(w, map[string]any{
		"state":  state.String(),
		"zone":   s.runMgr.Zone(),
		"team":   s.runMgr.Team(),
		"arena":  s.runMgr.Arena(),
		"mode":   s.runMgr.Mode(),
		"timing": s.runMgr.Timing(),
	})
}

func (s *Server) handleRunStart(w http.ResponseWriter, r *http.Request) {
	if s.runMgr.State() != run.StateReady {
		http.Error(w, "not ready", http.StatusConflict)
		return
	}

	zone := r.FormValue("zone")
	modeStr := r.FormValue("mode")
	arena := r.FormValue("arena")
	if zone == "" {
		zone = game.ZoneForTeam(game.Red)
	}
	if arena == "" {
		arena = gpio.DetectArena(s.cfg.ArenaUSBPath)
	}
	mode := run.ModeDevelopment
	if modeStr == "competition" {
		mode = run.ModeCompetition
	}

	if err := s.runMgr.StartUserCode(); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := s.runMgr.SendStartSignal(zone, arena, mode); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if mode == run.ModeCompetition {
		s.runMgr.StartReaper()
	}

	writeJSON(w, map[string]any{
		"status": "started",
		"team":   s.gameCfg.TeamForZone(zone),
		"zone":   zone,
		"timing": s.runMgr.Timing(),
	})
}

func (s *Server) handleRunReset(w http.ResponseWriter, r *http.Request) {
	s.runMgr.Reset()
	writeJSON(w, map[string]string{"status": "reset"})
}

func (s *Server) handleRunStop(w http.ResponseWriter, r *http.Request) {
	s.runMgr.Stop()
	writeJSON(w, map[string]string{"status": "stopped"})
}

// handleSysInfo serves the same hardware report printed at startup, so the web
// UI (or curl) can show what the brain thinks it is running on.
func (s *Server) handleSysInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, sysinfo.Collect(hardware.BoardStatus()))
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{
		"version":    sysinfo.Version,
		"commit":     sysinfo.Commit,
		"build_date": sysinfo.BuildDate,
	})
}

func (s *Server) handleGameInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{
		"config":  s.gameCfg,
		"teams":   game.Teams,
		"markers": game.Markers(),
		"scoring": game.ScoringRules(),
		"timing":  s.runMgr.Timing(),
	})
}

func (s *Server) handleGameScore(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Teams []game.TeamRound `json:"teams"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	writeJSON(w, map[string]any{"scores": game.ScoreRound(request.Teams)})
}

func (s *Server) handleGameLeague(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Scores []game.TeamScore `json:"scores"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	writeJSON(w, map[string]any{"standings": game.LeagueTable(request.Scores)})
}

func (s *Server) handleGameKnockout(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Entries []game.KnockoutEntry `json:"entries"`
	}
	if !decodeJSON(w, r, &request) {
		return
	}
	writeJSON(w, map[string]any{"advancing": game.AdvanceKnockout(request.Entries)})
}

func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func (s *Server) handleRunOutput(w http.ResponseWriter, r *http.Request) {
	// The web UI polls /run/output every second. ServeFile() emits
	// Last-Modified and answers 304 when mtime is unchanged within that
	// window (the brain's epoch RTC makes it worse), so the logs pane never
	// refreshes. Serve the file body unconditionally with caching disabled.
	w.Header().Set("Cache-Control", "no-store")
	data, err := os.ReadFile(s.cfg.OutputFilePath)
	if err != nil || len(data) == 0 {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.Write(data)
}

func (s *Server) handleRunTimeLeft(w http.ResponseWriter, r *http.Request) {
	left := s.runMgr.TimeLeft()
	timing := s.runMgr.Timing()
	response := map[string]any{"time_left": left, "timing": timing}
	if timing != nil {
		if timing.TimeLeftSeconds != nil {
			response["time_left_seconds"] = *timing.TimeLeftSeconds
		}
		response["snitch_window"] = timing.SnitchWindow
	}
	writeJSON(w, response)
}

func (s *Server) handleRunPicture(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, s.cfg.CameraImagePath)
}

// --- Upload handlers ---

func (s *Server) handleUploadIndex(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]string{"message": "Send a POST with a .py or .zip file to /api/upload"})
}

func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)

	file, header, err := r.FormFile("uploaded_file")
	if err != nil {
		http.Error(w, "missing file", http.StatusBadRequest)
		return
	}
	defer file.Close()

	if err := s.uploadH.ProcessUpload(file, header); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	writeJSON(w, map[string]string{"status": "ok"})
}

// --- Editor handlers ---

func (s *Server) handleEditorList(w http.ResponseWriter, r *http.Request) {
	list, err := s.editorH.ListFiles()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, list)
}

func (s *Server) handleEditorRead(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	content, err := s.editorH.ReadFile(filename)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, map[string]string{"filename": filename, "content": content})
}

func (s *Server) handleEditorWrite(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.editorH.WriteFile(filename, string(data)); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "saved"})
}

func (s *Server) handleEditorDelete(w http.ResponseWriter, r *http.Request) {
	filename := r.PathValue("filename")
	if err := s.editorH.DeleteFile(filename); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted"})
}

// --- WebSocket handlers ---

func (s *Server) handleWSCameraLogs(w http.ResponseWriter, r *http.Request) {
	// Single socket feeding both [CAMERA] and [LOGS] frames, exactly like the
	// legacy helper.py daemon the original web UI connects to on :5001.
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	frame := s.camStream.GetCurrentFrame()
	if frame != "" {
		conn.WriteMessage(websocket.TextMessage, []byte("[CAMERA]"+frame))
	}

	if err := s.wsHub.Add(conn); err != nil {
		conn.Close()
		return
	}
	defer s.wsHub.Remove(conn)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) handleWSCamera(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	frame := s.camStream.GetCurrentFrame()
	if frame != "" {
		conn.WriteMessage(websocket.TextMessage, []byte("[CAMERA]"+frame))
	}

	if err := s.wsHub.Add(conn); err != nil {
		conn.Close()
		return
	}
	defer s.wsHub.Remove(conn)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) handleWSLogs(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	if err := s.wsHub.Add(conn); err != nil {
		conn.Close()
		return
	}
	defer s.wsHub.Remove(conn)

	for {
		_, _, err := conn.ReadMessage()
		if err != nil {
			break
		}
	}
}

func (s *Server) handleWSPyls(w http.ResponseWriter, r *http.Request) {
	conn, err := s.upgrader.Upgrade(w, r, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.pylsProxy.Handle(conn)
}

// --- Static file handler ---

var staticCache sync.Map

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path

	// Match the original shepherd: / redirects to the editor app so its
	// relative bundle.js/style.css resolve against /editor/.
	if path == "/" {
		http.Redirect(w, r, "/editor/", http.StatusFound)
		return
	}

	// Check if path without trailing slash is a directory
	checkPath := strings.TrimSuffix(path, "/")
	if s.staticFS != nil {
		if f, err := s.staticFS.Open(strings.TrimPrefix(checkPath, "/")); err == nil {
			if stat, err := f.Stat(); err == nil && stat.IsDir() {
				f.Close()
				if !strings.HasSuffix(path, "/") {
					http.Redirect(w, r, path+"/", http.StatusMovedPermanently)
					return
				}
				path = path + "index.html"
			} else {
				f.Close()
			}
		}
	}

	path = strings.TrimPrefix(path, "/")

	if s.staticFS != nil {
		dataI, ok := staticCache.Load(path)
		if !ok {
			data, err := fs.ReadFile(s.staticFS, path)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			staticCache.Store(path, data)
			dataI = data
		}
		http.ServeContent(w, r, path, time.Time{}, bytes.NewReader(dataI.([]byte)))
		return
	}

	http.NotFound(w, r)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
