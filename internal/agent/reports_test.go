package agent

import (
	"strings"
	"testing"
)

func TestStripPreamble_RemovesLineBeforeVeredict(t *testing.T) {
	in := "Ya tengo los datos. Procedo.\n\n**Veredicto general:** ✅ Aprobado\n\n--- Título ---\n- Prefijo: ✅"
	got := stripPreamble(in)
	if !strings.HasPrefix(got, "**Veredicto general:**") {
		t.Errorf("output no empieza con veredicto: %q", got[:min(60, len(got))])
	}
	if strings.Contains(got, "Ya tengo los datos") {
		t.Errorf("preámbulo no se eliminó: %q", got)
	}
}

func TestStripPreamble_NoPreambleLeavesBodyIntact(t *testing.T) {
	in := "**Veredicto general:** ✅ Aprobado\n\n--- Título ---\n- Prefijo: ✅"
	got := stripPreamble(in)
	if got != in {
		t.Errorf("body sin preámbulo fue alterado: got %q, want %q", got, in)
	}
}

func TestStripPreamble_NoMarkerReturnsBodyUnchanged(t *testing.T) {
	in := "Texto sin marker en absoluto."
	got := stripPreamble(in)
	if got != in {
		t.Errorf("sin marker, esperaba body inalterado, got %q", got)
	}
}

func TestStripPreamble_MultilinePreamble(t *testing.T) {
	in := "Línea 1.\nLínea 2.\nLínea 3.\n\n**Veredicto general:** ⚠️ Aprobado con observaciones\n--- Título ---"
	got := stripPreamble(in)
	if !strings.HasPrefix(got, "**Veredicto general:**") {
		t.Errorf("output no empieza con veredicto: %q", got)
	}
	for _, line := range []string{"Línea 1", "Línea 2", "Línea 3"} {
		if strings.Contains(got, line) {
			t.Errorf("preámbulo %q sobrevivió: %q", line, got)
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
