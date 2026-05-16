package main

import (
	"regexp"
	"strings"
)

// orchestrateMarker es el contenido extraído del bloque <orchestrate>.
type orchestrateMarker struct {
	Kind    string // valor del atributo kind
	Payload string // JSON crudo entre los tags (sin trim por defecto)
}

// orchestrateRe matchea <orchestrate kind="..."> ... </orchestrate> en múltiples líneas.
// (?s) hace que . matchee newlines; (.*?) es lazy para no abarcar varios markers.
var orchestrateRe = regexp.MustCompile(`(?s)<orchestrate\s+kind\s*=\s*"([^"]+)"\s*>(.*?)</orchestrate>`)

// parseOrchestrate busca el primer marker <orchestrate kind="..."> en text.
// Devuelve el marker, la prosa restante (texto sin el bloque, trim de espacios),
// y ok=true si encontró un marker válido.
func parseOrchestrate(text string) (m orchestrateMarker, prose string, ok bool) {
	loc := orchestrateRe.FindStringSubmatchIndex(text)
	if loc == nil {
		return orchestrateMarker{}, text, false
	}
	kind := text[loc[2]:loc[3]]
	payload := text[loc[4]:loc[5]]
	prose = strings.TrimSpace(text[:loc[0]] + text[loc[1]:])
	return orchestrateMarker{Kind: kind, Payload: strings.TrimSpace(payload)}, prose, true
}
