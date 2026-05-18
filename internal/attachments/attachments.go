// Package attachments gestiona archivos subidos por el usuario para que el
// agente los procese en su próximo turno.
//
// Flujo:
//   1. Frontend hace POST multipart a /upload con uno o más archivos.
//   2. Store los guarda en disco bajo DefaultDir con un ID corto y devuelve
//      metadata.
//   3. Frontend manda /command con `attachments: [id, ...]` referenciando.
//   4. handleCommand llama Extract(id) por cada uno para obtener el texto
//      en markdown, y lo prepende al prompt del usuario.
//
// Soporta xlsx, csv/tsv, pdf, txt/md/json/log y imágenes (estas últimas
// emiten un warning porque el LLM actual no las procesa).
package attachments

import (
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ledongthuc/pdf"
	"github.com/xuri/excelize/v2"
)

const (
	DefaultDir       = "uploads"
	MaxBytesPerFile  = 10 * 1024 * 1024 // 10 MB
	MaxFilesPerBatch = 10
)

// Meta describe un attachment persistido.
type Meta struct {
	ID   string `json:"id"`
	Name string `json:"name"` // nombre original
	Size int64  `json:"size"`
	Kind string `json:"kind"` // xlsx | csv | tsv | pdf | text | image | unknown
}

// Store guarda y recupera attachments en disco. Concurrent-safe.
type Store struct {
	dir string
	mu  sync.Mutex
	// items en memoria para resolver ID → Meta rápido. Reconstruimos al
	// arrancar leyendo el dir; ver Load() en futuro si hace falta.
	items map[string]Meta
}

// New crea (o reusa) el directorio y devuelve un Store vacío.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creando %s: %w", dir, err)
	}
	return &Store{dir: dir, items: map[string]Meta{}}, nil
}

// SaveMultipart copia el contenido de un multipart.File al store y devuelve
// la metadata. Aplica límites de tamaño y rechaza nombres vacíos.
func (s *Store) SaveMultipart(fh *multipart.FileHeader) (Meta, error) {
	if fh.Size > MaxBytesPerFile {
		return Meta{}, fmt.Errorf("archivo %q excede el límite de %d bytes", fh.Filename, MaxBytesPerFile)
	}
	name := filepath.Base(fh.Filename) // anti-traversal
	if name == "" || name == "." || name == "/" {
		return Meta{}, errors.New("nombre de archivo vacío")
	}
	src, err := fh.Open()
	if err != nil {
		return Meta{}, fmt.Errorf("abriendo upload: %w", err)
	}
	defer src.Close()

	id := newID()
	// Guardamos en disco como "<id>__<nombre_original>" para que sea
	// debuggeable a ojo. El ID basta para resolver en futuro.
	stored := filepath.Join(s.dir, id+"__"+name)
	dst, err := os.OpenFile(stored, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return Meta{}, fmt.Errorf("creando archivo destino: %w", err)
	}
	written, err := io.Copy(dst, io.LimitReader(src, MaxBytesPerFile+1))
	dst.Close()
	if err != nil {
		os.Remove(stored)
		return Meta{}, fmt.Errorf("copiando: %w", err)
	}
	if written > MaxBytesPerFile {
		os.Remove(stored)
		return Meta{}, fmt.Errorf("archivo excede el límite")
	}

	meta := Meta{
		ID:   id,
		Name: name,
		Size: written,
		Kind: detectKind(name),
	}
	s.mu.Lock()
	s.items[id] = meta
	s.mu.Unlock()
	return meta, nil
}

// Lookup devuelve la Meta de un ID si existe.
func (s *Store) Lookup(id string) (Meta, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.items[id]
	return m, ok
}

// Extract lee el archivo y devuelve su contenido como markdown listo para
// inyectar en el prompt del LLM. Para imágenes devuelve un warning.
func (s *Store) Extract(id string) (string, error) {
	meta, ok := s.Lookup(id)
	if !ok {
		return "", fmt.Errorf("attachment %s no existe", id)
	}
	path := filepath.Join(s.dir, id+"__"+meta.Name)
	switch meta.Kind {
	case "xlsx":
		return extractXLSX(path, meta.Name)
	case "csv":
		return extractCSV(path, meta.Name, ',')
	case "tsv":
		return extractCSV(path, meta.Name, '\t')
	case "pdf":
		return extractPDF(path, meta.Name)
	case "text":
		return extractText(path, meta.Name)
	case "image":
		return fmt.Sprintf("[⚠ imagen %q (%d bytes) — el modelo actual no acepta imágenes, no la voy a poder analizar]",
			meta.Name, meta.Size), nil
	default:
		// Para extensiones desconocidas, intentamos leerla como texto.
		// Si parece binaria (no UTF-8), devolvemos solo metadata.
		return extractText(path, meta.Name)
	}
}

// detectKind clasifica por extensión. Lo hacemos por nombre porque el
// content-type del multipart no es confiable.
func detectKind(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".xlsx", ".xlsm":
		return "xlsx"
	case ".csv":
		return "csv"
	case ".tsv":
		return "tsv"
	case ".pdf":
		return "pdf"
	case ".txt", ".md", ".json", ".log", ".yaml", ".yml", ".xml", ".html", ".htm":
		return "text"
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return "image"
	}
	return "unknown"
}

// newID genera un ID corto único para el attachment.
func newID() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return "att-" + hex.EncodeToString(b[:])
}

// ─── Extractores ─────────────────────────────────────────────────────────────

func extractText(path, name string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Archivo adjunto: %s\n\n```\n", name)
	b.Write(data)
	b.WriteString("\n```\n")
	return b.String(), nil
}

func extractCSV(path, name string, sep rune) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.Comma = sep
	r.FieldsPerRecord = -1
	rows, err := r.ReadAll()
	if err != nil {
		return "", fmt.Errorf("parseando csv: %w", err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "### Archivo adjunto: %s (%d filas)\n\n", name, len(rows))
	writeMarkdownTable(&b, rows)
	return b.String(), nil
}

func extractXLSX(path, name string) (string, error) {
	f, err := excelize.OpenFile(path)
	if err != nil {
		return "", fmt.Errorf("abriendo xlsx: %w", err)
	}
	defer f.Close()
	sheets := f.GetSheetList()
	var b strings.Builder
	fmt.Fprintf(&b, "### Archivo adjunto: %s (%d hojas)\n\n", name, len(sheets))
	for _, sheet := range sheets {
		rows, err := f.GetRows(sheet)
		if err != nil {
			fmt.Fprintf(&b, "#### %s\n_(error leyendo hoja: %v)_\n\n", sheet, err)
			continue
		}
		fmt.Fprintf(&b, "#### Hoja: %s (%d filas)\n\n", sheet, len(rows))
		if len(rows) == 0 {
			b.WriteString("_(vacía)_\n\n")
			continue
		}
		writeMarkdownTable(&b, rows)
		b.WriteString("\n")
	}
	return b.String(), nil
}

func extractPDF(path, name string) (string, error) {
	f, r, err := pdf.Open(path)
	if err != nil {
		return "", fmt.Errorf("abriendo pdf: %w", err)
	}
	defer f.Close()
	var b strings.Builder
	fmt.Fprintf(&b, "### Archivo adjunto: %s (%d páginas)\n\n", name, r.NumPage())
	totalPages := r.NumPage()
	// Tope para no inundar el prompt: máximo 30 páginas por PDF.
	const maxPages = 30
	pages := totalPages
	if pages > maxPages {
		pages = maxPages
	}
	for i := 1; i <= pages; i++ {
		page := r.Page(i)
		if page.V.IsNull() {
			continue
		}
		text, err := page.GetPlainText(nil)
		if err != nil {
			continue
		}
		fmt.Fprintf(&b, "#### Página %d\n\n%s\n\n", i, strings.TrimSpace(text))
	}
	if totalPages > maxPages {
		fmt.Fprintf(&b, "_(truncado: PDF tiene %d páginas, solo se incluyen las primeras %d)_\n", totalPages, maxPages)
	}
	return b.String(), nil
}

// writeMarkdownTable formatea filas como tabla markdown. La primera fila es
// el header. Si la matriz tiene filas con distinto número de columnas,
// rellenamos con vacío. Trunca celdas largas para no romper el render.
func writeMarkdownTable(b *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	// Calcular ancho máximo por columna.
	maxCols := 0
	for _, r := range rows {
		if len(r) > maxCols {
			maxCols = len(r)
		}
	}
	if maxCols == 0 {
		return
	}
	// Tope para no escupir tablas gigantes al LLM: 100 filas + header.
	const maxRows = 101
	truncated := false
	if len(rows) > maxRows {
		rows = rows[:maxRows]
		truncated = true
	}
	writeRow(b, rows[0], maxCols)
	// separador
	b.WriteString("|")
	for i := 0; i < maxCols; i++ {
		b.WriteString("---|")
	}
	b.WriteString("\n")
	for _, r := range rows[1:] {
		writeRow(b, r, maxCols)
	}
	if truncated {
		fmt.Fprintf(b, "_(tabla truncada a %d filas)_\n", maxRows-1)
	}
}

func writeRow(b *strings.Builder, row []string, cols int) {
	b.WriteString("|")
	for i := 0; i < cols; i++ {
		cell := ""
		if i < len(row) {
			cell = row[i]
		}
		// Markdown necesita escape de | dentro de celda + colapsar newlines.
		cell = strings.ReplaceAll(cell, "|", "\\|")
		cell = strings.ReplaceAll(cell, "\n", " ")
		// Truncar celdas gigantes.
		const maxCell = 200
		if len(cell) > maxCell {
			cell = cell[:maxCell] + "…"
		}
		b.WriteString(" ")
		b.WriteString(cell)
		b.WriteString(" |")
	}
	b.WriteString("\n")
}

// ─── helpers serialización ───────────────────────────────────────────────────

// MetaJSON serializa Meta a JSON. Existe para tests; en producción se usa
// directamente el campo json tag del struct.
func MetaJSON(m Meta) string {
	b, _ := json.Marshal(m)
	return string(b)
}
