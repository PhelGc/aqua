package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	defaultSessionsDir  = "sessions"
	defaultSessionName  = "default"
	currentSessionFile  = ".current"
	sessionFileExt      = ".json"
)

var validSessionName = regexp.MustCompile(`^[a-zA-Z0-9._-]+$`)

type sessionFile struct {
	Name    string    `json:"name"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
	History []message `json:"history"`
}

type sessionManager struct {
	dir         string
	currentName string
}

func newSessionManager() (*sessionManager, error) {
	dir := os.Getenv("OPENCODE_SESSIONS_DIR")
	if dir == "" {
		dir = defaultSessionsDir
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creando %s: %w", dir, err)
	}
	s := &sessionManager{dir: dir}
	if name, err := s.readCurrent(); err == nil && name != "" {
		s.currentName = name
	} else {
		s.currentName = defaultSessionName
	}
	return s, nil
}

func (s *sessionManager) path(name string) string {
	return filepath.Join(s.dir, name+sessionFileExt)
}

func (s *sessionManager) readCurrent() (string, error) {
	data, err := os.ReadFile(filepath.Join(s.dir, currentSessionFile))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *sessionManager) writeCurrent() error {
	return os.WriteFile(filepath.Join(s.dir, currentSessionFile), []byte(s.currentName), 0o644)
}

func (s *sessionManager) current() string {
	return s.currentName
}

func (s *sessionManager) load(name string) ([]message, error) {
	if !validSessionName.MatchString(name) {
		return nil, fmt.Errorf("nombre inválido %q (solo letras, números, . _ -)", name)
	}
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var f sessionFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parseando sesión %q: %w", name, err)
	}
	return f.History, nil
}

func (s *sessionManager) save(name string, history []message) error {
	if !validSessionName.MatchString(name) {
		return fmt.Errorf("nombre inválido %q", name)
	}
	path := s.path(name)
	now := time.Now()
	f := sessionFile{
		Name:    name,
		Updated: now,
		History: history,
	}
	if existing, err := os.ReadFile(path); err == nil {
		var prev sessionFile
		if json.Unmarshal(existing, &prev) == nil && !prev.Created.IsZero() {
			f.Created = prev.Created
		}
	}
	if f.Created.IsZero() {
		f.Created = now
	}
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func (s *sessionManager) switchTo(name string) error {
	if !validSessionName.MatchString(name) {
		return fmt.Errorf("nombre inválido %q (solo letras, números, . _ -)", name)
	}
	s.currentName = name
	return s.writeCurrent()
}

func (s *sessionManager) list() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, sessionFileExt) {
			continue
		}
		names = append(names, strings.TrimSuffix(n, sessionFileExt))
	}
	sort.Strings(names)
	return names, nil
}

func (s *sessionManager) delete(name string) error {
	if name == s.currentName {
		return fmt.Errorf("no se puede borrar la sesión actual (cambiá primero con /sessions load)")
	}
	if !validSessionName.MatchString(name) {
		return fmt.Errorf("nombre inválido %q", name)
	}
	err := os.Remove(s.path(name))
	if os.IsNotExist(err) {
		return fmt.Errorf("la sesión %q no existe", name)
	}
	return err
}
