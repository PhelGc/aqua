// Package skills carga y renderiza las skills (templates .md) que el usuario
// invoca con `/<nombre>`.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	defaultSkillsDir = "skills"
	inputPlaceholder = "{{input}}"
)

var utf8BOM = string([]byte{0xEF, 0xBB, 0xBF})

// nameNormalizer reemplaza acentos comunes y mapea a minúsculas para que
// /recordá y /recorda matcheen el mismo skill (no se complica la vida el usuario
// tipeando acentos en una terminal).
var nameNormalizer = strings.NewReplacer(
	"á", "a", "é", "e", "í", "i", "ó", "o", "ú", "u",
	"Á", "a", "É", "e", "Í", "i", "Ó", "o", "Ú", "u",
	"ñ", "n", "Ñ", "n", "ü", "u", "Ü", "u",
)

func normalizeName(s string) string {
	return strings.ToLower(nameNormalizer.Replace(s))
}

type Skill struct {
	Name        string
	Description string
	Template    string
}

type Registry struct {
	skills map[string]*Skill
}

func Load() (*Registry, error) {
	dir := os.Getenv("OPENCODE_SKILLS_DIR")
	if dir == "" {
		dir = defaultSkillsDir
	}
	r := &Registry{skills: make(map[string]*Skill)}
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
		r.skills[normalizeName(name)] = &Skill{
			Name:        name,
			Description: desc,
			Template:    body,
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

func (r *Registry) Render(name, args string) (string, bool) {
	s, ok := r.skills[normalizeName(name)]
	if !ok {
		return "", false
	}
	body := substituteTimeTokens(s.Template)
	body = r.substituteSkillsToken(body)
	if strings.Contains(body, inputPlaceholder) {
		return strings.ReplaceAll(body, inputPlaceholder, args), true
	}
	if args == "" {
		return body, true
	}
	return body + "\n\n" + args, true
}

// substituteSkillsToken reemplaza {{skills}} por la lista de skills cargadas.
// Útil para skills meta como /schedule que necesitan saber qué comandos puede
// programar.
func (r *Registry) substituteSkillsToken(template string) string {
	if !strings.Contains(template, "{{skills}}") {
		return template
	}
	names := make([]string, 0, len(r.skills))
	for _, s := range r.skills {
		names = append(names, "/"+s.Name)
	}
	sort.Strings(names)
	return strings.ReplaceAll(template, "{{skills}}", strings.Join(names, ", "))
}

var weekdayEs = map[time.Weekday]string{
	time.Sunday:    "domingo",
	time.Monday:    "lunes",
	time.Tuesday:   "martes",
	time.Wednesday: "miércoles",
	time.Thursday:  "jueves",
	time.Friday:    "viernes",
	time.Saturday:  "sábado",
}

// substituteTimeTokens reemplaza placeholders temporales en el template del skill:
//
//	{{now}}     → ISO8601 con offset local (ej. 2026-05-17T19:35:42-05:00)
//	{{today}}   → fecha YYYY-MM-DD
//	{{weekday}} → día de la semana en español
//
// Se resuelven en cada render, así las skills siempre ven la hora actual.
func substituteTimeTokens(template string) string {
	now := time.Now()
	template = strings.ReplaceAll(template, "{{now}}", now.Format(time.RFC3339))
	template = strings.ReplaceAll(template, "{{today}}", now.Format("2006-01-02"))
	template = strings.ReplaceAll(template, "{{weekday}}", weekdayEs[now.Weekday()])
	return template
}

func (r *Registry) List() []*Skill {
	out := make([]*Skill, 0, len(r.skills))
	for _, s := range r.skills {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
