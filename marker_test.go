package main

import (
	"strings"
	"testing"
)

func TestParseOrchestrate_BasicMatch(t *testing.T) {
	in := `<orchestrate kind="report">{"jql":"a","max":10}</orchestrate>`
	m, prose, ok := parseOrchestrate(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if m.Kind != "report" {
		t.Errorf("Kind = %q, want %q", m.Kind, "report")
	}
	if m.Payload != `{"jql":"a","max":10}` {
		t.Errorf("Payload = %q", m.Payload)
	}
	if prose != "" {
		t.Errorf("Prose = %q, want empty", prose)
	}
}

func TestParseOrchestrate_MultilinePayload(t *testing.T) {
	in := "<orchestrate kind=\"report\">\n{\n  \"jql\": \"x\",\n  \"max\": 5\n}\n</orchestrate>"
	m, _, ok := parseOrchestrate(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if !strings.Contains(m.Payload, "\"jql\": \"x\"") {
		t.Errorf("Payload no contiene jql: %q", m.Payload)
	}
}

func TestParseOrchestrate_NoMarker(t *testing.T) {
	in := "respuesta normal sin marker"
	_, prose, ok := parseOrchestrate(in)
	if ok {
		t.Error("expected ok=false")
	}
	if prose != in {
		t.Errorf("Prose = %q, want %q", prose, in)
	}
}

func TestParseOrchestrate_ProseAroundMarker(t *testing.T) {
	in := `Ya casi.
<orchestrate kind="report">{"jql":"a"}</orchestrate>
Listo.`
	m, prose, ok := parseOrchestrate(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if m.Kind != "report" {
		t.Errorf("Kind = %q", m.Kind)
	}
	if !strings.Contains(prose, "Ya casi") || !strings.Contains(prose, "Listo") {
		t.Errorf("prose perdió contenido: %q", prose)
	}
	if strings.Contains(prose, "orchestrate") {
		t.Errorf("prose todavía contiene el marker: %q", prose)
	}
}

func TestParseOrchestrate_FirstOfMultiple(t *testing.T) {
	in := `<orchestrate kind="report">A</orchestrate><orchestrate kind="classify">B</orchestrate>`
	m, _, ok := parseOrchestrate(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if m.Kind != "report" {
		t.Errorf("Kind = %q, want first marker (report)", m.Kind)
	}
	if m.Payload != "A" {
		t.Errorf("Payload = %q, want A", m.Payload)
	}
}

func TestParseOrchestrate_WhitespaceInTag(t *testing.T) {
	in := `<orchestrate  kind = "report"  >payload</orchestrate>`
	m, _, ok := parseOrchestrate(in)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if m.Kind != "report" || m.Payload != "payload" {
		t.Errorf("Kind=%q Payload=%q", m.Kind, m.Payload)
	}
}

func TestParseOrchestrate_PayloadTrimmed(t *testing.T) {
	in := `<orchestrate kind="report">   {"a":1}   </orchestrate>`
	m, _, _ := parseOrchestrate(in)
	if m.Payload != `{"a":1}` {
		t.Errorf("Payload = %q, expected trimmed", m.Payload)
	}
}
