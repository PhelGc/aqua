package web

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeReportPath(t *testing.T) {
	base := "reports"
	absBase, _ := filepath.Abs(base)

	tests := []struct {
		name    string
		rel     string
		wantOK  bool
		wantSub string // si wantOK, el resultado debe empezar con absBase + sep
	}{
		{"archivo simple", "2026-05-17-test.md", true, absBase},
		{"subdirectorio legítimo", "subdir/foo.md", true, absBase},
		{"nombre con doble punto legítimo", "foo..bar.md", true, absBase},

		{"vacío", "", false, ""},
		{"traversal arriba", "../etc/passwd.md", false, ""},
		{"traversal arriba con subdir", "subdir/../../etc.md", false, ""},
		{"abs unix", "/etc/passwd.md", false, ""},
		{"abs windows", `C:\Windows\foo.md`, false, ""},
		{"sin extensión md", "secret", false, ""},
		{"extensión .env", "secret.env", false, ""},
		{"json", "config.json", false, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := safeReportPath(base, tc.rel)
			if ok != tc.wantOK {
				t.Fatalf("safeReportPath(%q, %q) ok=%v want %v (got=%q)",
					base, tc.rel, ok, tc.wantOK, got)
			}
			if ok && !strings.HasPrefix(got, tc.wantSub) {
				t.Errorf("path %q no empieza con %q", got, tc.wantSub)
			}
		})
	}
}

// safeReportPath debe normalizar separadores de URL (/) a los del OS.
// En Windows, "subdir/foo.md" tiene que resolver a "subdir\foo.md" dentro
// de base; en Linux queda igual.
func TestSafeReportPath_NormalizesSlashes(t *testing.T) {
	got, ok := safeReportPath("reports", "a/b/c.md")
	if !ok {
		t.Fatal("debería aceptar a/b/c.md")
	}
	want := filepath.Join("a", "b", "c.md")
	if !strings.HasSuffix(got, want) {
		t.Errorf("path %q no termina con %q (separadores OS)", got, want)
	}
}
