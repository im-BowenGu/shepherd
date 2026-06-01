package ws

import (
	"io"
	"log"
	"os"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type LogStreamer struct {
	hub    *Hub
	logPath string
	mu     sync.Mutex
	f      *os.File
	offset int64
}

func NewLogStreamer(hub *Hub, logPath string) *LogStreamer {
	return &LogStreamer{
		hub:     hub,
		logPath: logPath,
	}
}

func (l *LogStreamer) Start() error {
	var err error
	l.f, err = os.Open(l.logPath)
	if err != nil {
		return err
	}

	l.offset, _ = l.f.Seek(0, io.SeekEnd)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		l.f.Close()
		return err
	}

	if err := watcher.Add(l.logPath); err != nil {
		watcher.Close()
		l.f.Close()
		return err
	}

	go func() {
		defer watcher.Close()

		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
					l.processAndBroadcast()
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("log watcher error: %v", err)
			}
		}
	}()

	return nil
}

func (l *LogStreamer) processAndBroadcast() {
	l.mu.Lock()
	defer l.mu.Unlock()

	newData, err := io.ReadAll(l.f)
	if err != nil {
		log.Printf("logs: read: %v", err)
		return
	}
	l.offset += int64(len(newData))

	if len(newData) > 0 {
		l.hub.Broadcast([]byte("[LOGS]" + string(newData)))
	}
}
