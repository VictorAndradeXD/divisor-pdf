package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFalhaAnterior(t *testing.T) {
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	os.MkdirAll(filepath.Join(cache, "DivisorPDF"), 0o755)
	os.WriteFile(filepath.Join(cache, "DivisorPDF", "ultima-falha.txt"), []byte("fatal error: runtime: out of memory\n"), 0o644)
	txt := iniciarRegistro()
	titulo, corpo := explicarFalhaAnterior(txt)
	t.Log(titulo, "|", corpo)
	if !strings.Contains(corpo, "memória") || !strings.Contains(corpo, "divisor.log") {
		t.Fatal("texto inesperado")
	}
	if b, _ := os.ReadFile(filepath.Join(cache, "DivisorPDF", "ultima-falha.txt")); len(b) != 0 {
		t.Fatal("arquivo de falha não foi zerado")
	}
	log, _ := os.ReadFile(filepath.Join(cache, "DivisorPDF", "divisor.log"))
	if !strings.Contains(string(log), "out of memory") {
		t.Fatal("falha não foi para o registro")
	}
}
