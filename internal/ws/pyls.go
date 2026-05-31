package ws

import (
	"bufio"
	"encoding/json"
	"log"
	"os/exec"

	"github.com/gorilla/websocket"
)

type PylsProxy struct {
	robotLibPath string
}

func NewPylsProxy(robotLibPath string) *PylsProxy {
	return &PylsProxy{robotLibPath: robotLibPath}
}

func (p *PylsProxy) Handle(conn *websocket.Conn) {
	defer conn.Close()

	cmd := exec.Command("python3", "-m", "pyls", "-v")
	cmd.Env = append(cmd.Environ(), "PYTHONPATH="+p.robotLibPath)

	stdin, err := cmd.StdinPipe()
	if err != nil {
		log.Printf("pyls stdin pipe: %v", err)
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		log.Printf("pyls stdout pipe: %v", err)
		return
	}

	if err := cmd.Start(); err != nil {
		log.Printf("pyls start: %v", err)
		return
	}

	done := make(chan struct{})

	go func() {
		defer close(done)
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			msg := scanner.Bytes()
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				cmd.Process.Kill()
				return
			}
			var parsed any
			if err := json.Unmarshal(msg, &parsed); err == nil {
				serialized, _ := json.Marshal(parsed)
				stdin.Write(serialized)
			}
		}
	}()

	<-done
	cmd.Wait()
}
