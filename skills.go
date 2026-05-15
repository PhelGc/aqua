package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultSkillsDir = "skills"
	inputPlaceholder = "{{input}}"
)

var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

type skill struct {
	name        string
	description string
	template    string
}

type skillRegistry struct {
	skills map[string]*skill
}

func loadSkills() (*skillRegistry, error) {
	dir := os.Getenv("OPENCODE_SKILLS_DIR")
	if dir == "" {
		dir = defaultSkillsDir
	}
	r := &skillRegistry{skills: make(map[string]*skill)}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".md") {
			continue
		}
		name := strings.TrimSuffix(e.Name(), filepath.Ext(e.Name()))
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("leyendo skill %q: %w", name, err)
		}
		desc, body := parseSkillFile(string(data))
		r.skills[name] = &skill{
			name:        name,
			description: desc,
			template:    body,
		}
	}
	return r, nil
}

func parseSkillFile(content string) (description, body string) {
	content = strings.TrimPrefix(content, utf8BOM)
	if !strings.HasPrefix(content, "---") {
		return "", strings.TrimSpace(content)
	}
	rest := strings.TrimPrefix(content, "---")
	rest = strings.TrimLeft(rest, "\r\n")
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return "", strings.TrimSpace(content)
	}
	front := rest[:end]
	body = strings.TrimSpace(rest[end+len("\n---"):])
	for _, line := range strings.Split(front, "\n") {
		line = strings.TrimSpace(line)
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "description" {
			description = strings.Trim(strings.TrimSpace(v), "\"'")
		}
	}
	return description, body
}

func (r *skillRegistry) render(name, args string) (string, bool) {
	s, ok := r.skills[name]
	if !ok {
		return "", false
	}
	if strings.Contains(s.template, inputPlaceholder) {
		return strings.ReplaceAll(s.template, inputPlaceholder, args), true
	}
	if args == "" {
		return s.template, true
	}
	return s.template + "\n\n" + args, true
}

func (r *skillRegistry) list() []*skill {
	out := make([]*skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}
