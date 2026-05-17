package main

import (
	"fmt"
	"os"
	"strings"
)

const defaultMemoryPath = "memory.md"

// loadMemory lee el archivo de memoria persistente. Si no existe, devuelve ""
// sin error (la memoria es opcional). Path por env OPENCODE_MEMORY_PATH;
// default: memory.md en el cwd.
//
// Se llama en cada turn desde runConversation para que cambios escritos por
// aqua via fs__write_file durante una conversación queden visibles en el
// siguiente turn.
func loadMemory() (string, error) {
	path := os.Getenv("OPENCODE_MEMORY_PATH")
	if path == "" {
		path = defaultMemoryPath
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", fmt.Errorf("leyendo %s: %w", path, err)
	}
	return strings.TrimSpace(string(data)), nil
}
