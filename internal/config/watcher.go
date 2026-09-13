package config

import (
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Reloader struct {
	mu       sync.RWMutex
	current  *Config
	path     string
	onChange func(*Config)
}

func NewReloader(path string, onChange func(*Config)) (*Reloader, error) {
	r := &Reloader{path: path, onChange: onChange}
	cfg, err := LoadConfig(path)
	if err != nil {
		return nil, err
	}
	r.current = cfg
	go r.watch()
	return r, nil
}

func (r *Reloader) Current() *Config {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.current
}

func (r *Reloader) watch() {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("config: fsnotify init failed: %v", err)
		return
	}
	defer watcher.Close()

	dir := filepath.Dir(r.path)
	if err := watcher.Add(dir); err != nil {
		log.Printf("config: watch %s failed: %v", dir, err)
		return
	}

	debounce := time.NewTimer(0)
	if !debounce.Stop() {
		<-debounce.C
	}
	var pending bool

	for {
		select {
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Base(event.Name) != filepath.Base(r.path) {
				continue
			}
			if event.Op&(fsnotify.Write|fsnotify.Create) == 0 {
				continue
			}
			pending = true
			debounce.Reset(200 * time.Millisecond)

		case _, ok := <-watcher.Errors:
			if !ok {
				return
			}

		case <-debounce.C:
			if !pending {
				continue
			}
			pending = false
			cfg, err := LoadConfig(r.path)
			if err != nil {
				log.Printf("config: reload failed: %v", err)
				continue
			}
			r.mu.Lock()
			r.current = cfg
			r.mu.Unlock()
			log.Printf("config: reloaded successfully")
			if r.onChange != nil {
				r.onChange(cfg)
			}
		}
	}
}
