package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"divisorpdf/divisor"
)

// O registro fica em <cache do usuário>/DivisorPDF (no Windows,
// %LOCALAPPDATA%\DivisorPDF): divisor.log com cada divisão e
// ultima-falha.txt com a saída do Go se o programa morrer (falta de
// memória, por exemplo), que não passaria por nenhum recover.
var registro struct {
	sync.Mutex
	pasta   string
	arquivo *os.File
}

const limiteRegistro = 1 << 20

// iniciarRegistro abre o log e devolve o texto da falha da execução anterior,
// se houve uma.
func iniciarRegistro() (falhaAnterior string) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	pasta := filepath.Join(base, "DivisorPDF")
	if err := os.MkdirAll(pasta, 0o755); err != nil {
		return ""
	}
	registro.pasta = pasta

	log := filepath.Join(pasta, "divisor.log")
	if st, err := os.Stat(log); err == nil && st.Size() > limiteRegistro {
		_ = os.Rename(log, log+".old")
	}
	if f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		registro.arquivo = f
	}

	falha := filepath.Join(pasta, "ultima-falha.txt")
	if b, err := os.ReadFile(falha); err == nil && len(strings.TrimSpace(string(b))) > 0 {
		falhaAnterior = string(b)
		registrar("execução anterior terminou inesperadamente:\n%s", b)
	}
	if f, err := os.Create(falha); err == nil { // trunca: só guarda a falha desta execução
		_ = debug.SetCrashOutput(f, debug.CrashOptions{})
	}
	return falhaAnterior
}

func caminhoRegistro() string {
	if registro.pasta == "" {
		return ""
	}
	return filepath.Join(registro.pasta, "divisor.log")
}

func registrar(formato string, args ...any) {
	registro.Lock()
	defer registro.Unlock()
	if registro.arquivo == nil {
		return
	}
	fmt.Fprintf(registro.arquivo, "%s  %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(formato, args...))
}

// erroAmigavel é o que a tela mostra quando algo dá errado.
type erroAmigavel struct {
	Titulo  string
	Texto   string
	Tecnico string
}

// explicar traduz um erro em uma mensagem que diz o que fazer.
func explicar(err error) erroAmigavel {
	e := erroAmigavel{Tecnico: err.Error()}
	var interno *divisor.ErroInterno
	minusculo := strings.ToLower(err.Error())

	switch {
	case errors.Is(err, divisor.ErrNaoEhPDF):
		e.Titulo = "Este arquivo não é um PDF"
		e.Texto = "O conteúdo não começa como um PDF. Confira se escolheu o arquivo certo; às vezes a extensão .pdf foi dada a outro tipo de arquivo."
	case errors.As(err, &interno):
		e.Titulo = "O PDF tem uma estrutura que o programa não conseguiu processar"
		e.Texto = "O arquivo pode estar danificado. Se ele abre normalmente no leitor de PDF, tente salvá-lo de novo por lá (\"Salvar como\" ou \"Imprimir para PDF\") e divida a cópia."
		e.Tecnico += "\n\n" + string(interno.Pilha)
	case strings.Contains(minusculo, "password") || strings.Contains(minusculo, "encrypt") || strings.Contains(minusculo, "senha"):
		e.Titulo = "O PDF está protegido por senha"
		e.Texto = "Abra o arquivo no leitor de PDF com a senha, salve uma cópia sem proteção e divida essa cópia."
	case ehDiscoCheio(err):
		e.Titulo = "O disco ficou cheio"
		e.Texto = "Não há espaço suficiente na pasta de saída. Libere espaço ou escolha outra pasta (outro disco ou pendrive) em \"Salvar em\"."
	case ehEmUso(err):
		e.Titulo = "Um arquivo está aberto em outro programa"
		e.Texto = "Feche o PDF (e as partes já geradas) no leitor de PDF ou em outros programas e tente de novo."
	case errors.Is(err, fs.ErrPermission):
		e.Titulo = "Sem permissão para ler ou gravar"
		e.Texto = "O sistema bloqueou o acesso ao arquivo ou à pasta de saída. Escolha outra pasta em \"Salvar em\" (por exemplo, Documentos)."
	case errors.Is(err, fs.ErrNotExist):
		e.Titulo = "Arquivo não encontrado"
		e.Texto = "O PDF foi movido, renomeado ou está num disco que foi desconectado."
	default:
		e.Titulo = "Não foi possível dividir o PDF"
		e.Texto = "O arquivo pode estar danificado ou usar um recurso que o programa não suporta. Os detalhes abaixo ajudam a investigar."
	}
	return e
}

// explicarFalhaAnterior resume o conteúdo de ultima-falha.txt.
func explicarFalhaAnterior(texto string) (titulo, corpo string) {
	titulo = "O programa fechou inesperadamente na última vez"
	onde := ""
	if r := caminhoRegistro(); r != "" {
		onde = " Os detalhes foram salvos em " + r + "."
	}
	if strings.Contains(texto, "out of memory") || strings.Contains(texto, "cannot allocate memory") {
		return titulo, "A memória do computador acabou durante a divisão. Feche outros programas (principalmente o navegador) e tente de novo." + onde
	}
	return titulo, "Não foi possível identificar a causa." + onde + " Se acontecer de novo, envie esse arquivo para quem mantém o programa."
}
