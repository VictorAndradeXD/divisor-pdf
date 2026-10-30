package main

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"gioui.org/io/event"
	"github.com/godbus/dbus/v5"
)

// O Gio não entrega ao programa arquivos soltos sobre a janela no Wayland.
const suportaArrastar = false

// escolherArquivo e escolherPasta abrem o seletor nativo do desktop pelo
// xdg-desktop-portal. Bloqueiam até o usuário responder.
func escolherArquivo(_ uintptr, atual string) (string, bool, error) {
	return portal("Escolha o PDF a ser dividido", false, filepath.Dir(atual))
}

func escolherPasta(_ uintptr, atual string) (string, bool, error) {
	return portal("Pasta onde os arquivos divididos serão gravados", true, atual)
}

func portal(titulo string, pasta bool, inicial string) (string, bool, error) {
	conn, err := dbus.SessionBus()
	if err != nil {
		return "", false, fmt.Errorf("sem acesso ao barramento de sessão: %w", err)
	}

	// O portal responde com um sinal num objeto cujo caminho deriva do nosso
	// nome no barramento e de um token; a inscrição vem antes da chamada para
	// não perder uma resposta rápida.
	token := fmt.Sprintf("divisorpdf%d", rand.Uint32())
	remetente := strings.ReplaceAll(strings.TrimPrefix(conn.Names()[0], ":"), ".", "_")
	pedido := dbus.ObjectPath("/org/freedesktop/portal/desktop/request/" + remetente + "/" + token)
	filtro := []dbus.MatchOption{
		dbus.WithMatchObjectPath(pedido),
		dbus.WithMatchInterface("org.freedesktop.portal.Request"),
		dbus.WithMatchMember("Response"),
	}
	if err := conn.AddMatchSignal(filtro...); err != nil {
		return "", false, err
	}
	defer conn.RemoveMatchSignal(filtro...)
	sinais := make(chan *dbus.Signal, 4)
	conn.Signal(sinais)
	defer conn.RemoveSignal(sinais)

	type padrao struct {
		Tipo   uint32 // 0 = glob, 1 = tipo MIME
		Padrao string
	}
	type filtroArquivo struct {
		Nome    string
		Padroes []padrao
	}
	opcoes := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token),
		"modal":        dbus.MakeVariant(true),
	}
	if pasta {
		opcoes["directory"] = dbus.MakeVariant(true)
	} else {
		pdf := filtroArquivo{"Arquivos PDF", []padrao{{0, "*.pdf"}, {0, "*.PDF"}, {1, "application/pdf"}}}
		todos := filtroArquivo{"Todos os arquivos", []padrao{{0, "*"}}}
		opcoes["filters"] = dbus.MakeVariant([]filtroArquivo{pdf, todos})
		opcoes["current_filter"] = dbus.MakeVariant(pdf)
	}
	if inicial != "" && inicial != "." {
		opcoes["current_folder"] = dbus.MakeVariant(append([]byte(inicial), 0))
	}

	obj := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var resposta dbus.ObjectPath
	if err := obj.Call("org.freedesktop.portal.FileChooser.OpenFile", 0, "", titulo, opcoes).Store(&resposta); err != nil {
		return "", false, fmt.Errorf("o seletor de arquivos (xdg-desktop-portal) não respondeu: %w", err)
	}

	for s := range sinais {
		if s.Path != resposta && s.Path != pedido {
			continue
		}
		if len(s.Body) < 2 {
			return "", false, errors.New("resposta inválida do seletor de arquivos")
		}
		if codigo, _ := s.Body[0].(uint32); codigo != 0 {
			return "", false, nil // cancelado
		}
		resultados, _ := s.Body[1].(map[string]dbus.Variant)
		uris, _ := resultados["uris"].Value().([]string)
		if len(uris) == 0 {
			return "", false, nil
		}
		u, err := url.Parse(uris[0])
		if err != nil || u.Scheme != "file" {
			return "", false, fmt.Errorf("o seletor devolveu um endereço inesperado: %s", uris[0])
		}
		return u.Path, true, nil
	}
	return "", false, errors.New("o seletor de arquivos foi fechado")
}

// mostrarArquivo abre o gerenciador de arquivos com o arquivo selecionado,
// quando ele aceita (Nautilus e outros); senão, só abre a pasta.
func mostrarArquivo(caminho string) {
	if conn, err := dbus.SessionBus(); err == nil {
		u := url.URL{Scheme: "file", Path: caminho}
		obj := conn.Object("org.freedesktop.FileManager1", "/org/freedesktop/FileManager1")
		if obj.Call("org.freedesktop.FileManager1.ShowItems", 0, []string{u.String()}, "").Err == nil {
			return
		}
	}
	abrirPasta(filepath.Dir(caminho))
}

func abrirPasta(pasta string) {
	_ = exec.Command("xdg-open", pasta).Start()
}

func espacoLivre(pasta string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(pasta, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func ehDiscoCheio(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

func ehEmUso(err error) bool {
	return errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.ETXTBSY)
}

// notificar avisa o fim da divisão quando a janela não está em foco.
func notificar(_ uintptr, titulo, texto string) {
	_ = exec.Command("notify-send", "-a", "Divisor de PDF", titulo, texto).Start()
}

func (t *tela) eventoNativo(event.Event) {}

func progressoTarefa(uintptr, estadoTarefa, float64) {}
