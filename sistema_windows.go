package main

import (
	"errors"

	"gioui.org/app"
	"gioui.org/io/event"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"github.com/rodrigocfd/windigo/x/cosh"
	"github.com/rodrigocfd/windigo/x/winsh"
	"golang.org/x/sys/windows"
)

const suportaArrastar = true

func escolherArquivo(hwnd uintptr, atual string) (string, bool, error) {
	if atual != "" {
		atual = filepath.Dir(atual)
	}
	c, ok := escolher(hwnd, false, atual)
	return c, ok, nil
}

func escolherPasta(hwnd uintptr, atual string) (string, bool, error) {
	c, ok := escolher(hwnd, true, atual)
	return c, ok, nil
}

// escolher abre o diálogo nativo para escolher um PDF ou, se pasta, uma pasta.
// O COM exige uma thread do sistema fixa e inicializada.
func escolher(hwnd uintptr, pasta bool, atual string) (string, bool) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	_, _ = win.CoInitializeEx(co.COINIT_APARTMENTTHREADED | co.COINIT_DISABLE_OLE1DDE)
	defer win.CoUninitialize()

	rel := win.NewOleReleaser()
	defer rel.Release()

	var fod *winsh.IFileOpenDialog
	if err := win.CoCreateInstance(rel, &cosh.CLSID_FileOpenDialog, nil, co.CLSCTX_INPROC_SERVER, &fod); err != nil {
		return "", false
	}
	opts, _ := fod.GetOptions()
	if pasta {
		_ = fod.SetOptions(opts | cosh.FOS_FORCEFILESYSTEM | cosh.FOS_PICKFOLDERS)
		_ = fod.SetTitle("Pasta onde os arquivos divididos serão gravados")
	} else {
		_ = fod.SetOptions(opts | cosh.FOS_FORCEFILESYSTEM | cosh.FOS_FILEMUSTEXIST)
		_ = fod.SetFileTypes([]winsh.COMDLG_FILTERSPEC{
			{Name: "Arquivos PDF", Spec: "*.pdf"},
			{Name: "Todos os arquivos", Spec: "*.*"},
		})
		_ = fod.SetFileTypeIndex(1)
		_ = fod.SetTitle("Escolha o PDF a ser dividido")
	}
	if atual != "" {
		var item *winsh.IShellItem
		if winsh.SHCreateItemFromParsingName(rel, atual, &item) == nil {
			_ = fod.SetFolder(item)
		}
	}

	if ok, _ := fod.Show(win.HWND(hwnd)); !ok {
		return "", false
	}
	item, err := fod.GetResult(rel)
	if err != nil {
		return "", false
	}
	caminho, err := item.GetDisplayName(cosh.SIGDN_FILESYSPATH)
	return caminho, err == nil
}

// mostrarArquivo abre o Explorer com o arquivo selecionado. A linha de
// comando vai pronta porque o explorer.exe não entende as aspas que o Go
// colocaria em volta de "/select,caminho com espaços".
func mostrarArquivo(caminho string) {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + caminho + `"`}
	_ = cmd.Start()
}

func abrirPasta(pasta string) {
	cmd := exec.Command("explorer.exe")
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe "` + pasta + `"`}
	_ = cmd.Start()
}

func espacoLivre(pasta string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(pasta)
	if err != nil {
		return 0, err
	}
	var livre, total, totalLivre uint64
	if err := windows.GetDiskFreeSpaceEx(p, &livre, &total, &totalLivre); err != nil {
		return 0, err
	}
	return livre, nil
}

func ehDiscoCheio(err error) bool {
	return errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL)
}

func ehEmUso(err error) bool {
	return errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION)
}

// notificar pisca o botão na barra de tarefas até a janela receber foco.
func notificar(hwnd uintptr, _, _ string) {
	if hwnd == 0 {
		return
	}
	fi := win.FLASHWINFO{Hwnd: win.HWND(hwnd), DwFlags: co.FLASHW_TRAY | co.FLASHW_TIMERNOFG}
	fi.SetCbSize()
	win.FlashWindowEx(&fi)
}

// O Gio não trata WM_DROPFILES; a janela dele é subclassificada para aceitar
// PDFs arrastados do Explorer.
var arrastar struct {
	procOriginal uintptr
	aoSoltar     func(string)
}

func instalarArrastar(hwnd uintptr, aoSoltar func(string)) {
	if hwnd == 0 || arrastar.procOriginal != 0 {
		return
	}
	arrastar.aoSoltar = aoSoltar
	h := win.HWND(hwnd)
	winsh.DragAcceptFiles(h, true)
	proc := syscall.NewCallback(func(hwnd, msg, wp, lp uintptr) uintptr {
		if co.WM(msg) == co.WM_DROPFILES {
			hDrop := win.HDROP(wp)
			if arquivos, err := hDrop.DragQueryFile(); err == nil && len(arquivos) > 0 {
				arrastar.aoSoltar(arquivos[0])
			}
			hDrop.DragFinish()
			return 0
		}
		return win.CallWindowProc(arrastar.procOriginal, win.HWND(hwnd), co.WM(msg), win.WPARAM(wp), win.LPARAM(lp))
	})
	arrastar.procOriginal, _ = h.SetWindowLongPtr(co.GWLP_WNDPROC, proc)
}

// Progresso no botão da barra de tarefas. O ITaskbarList3 vive numa thread
// própria com COM inicializado; os pedidos chegam por um canal.
var tarefa struct {
	pedidos chan pedidoTarefa
}

type pedidoTarefa struct {
	hwnd   uintptr
	estado estadoTarefa
	fracao float64
}

func progressoTarefa(hwnd uintptr, estado estadoTarefa, fracao float64) {
	if hwnd == 0 {
		return
	}
	if tarefa.pedidos == nil {
		tarefa.pedidos = make(chan pedidoTarefa, 16)
		go servirTarefa(tarefa.pedidos)
	}
	select {
	case tarefa.pedidos <- pedidoTarefa{hwnd, estado, fracao}:
	default: // a barra de tarefas é só um enfeite; não segura a interface
	}
}

func servirTarefa(pedidos chan pedidoTarefa) {
	runtime.LockOSThread()
	_, _ = win.CoInitializeEx(co.COINIT_APARTMENTTHREADED | co.COINIT_DISABLE_OLE1DDE)
	rel := win.NewOleReleaser()
	var tb *winsh.ITaskbarList3
	if win.CoCreateInstance(rel, &cosh.CLSID_TaskbarList, nil, co.CLSCTX_INPROC_SERVER, &tb) != nil || tb.HrInit() != nil {
		for range pedidos {
		}
		return
	}
	for p := range pedidos {
		h := win.HWND(p.hwnd)
		switch p.estado {
		case tarefaNenhuma:
			_ = tb.SetProgressState(h, cosh.TBPF_NOPROGRESS)
		case tarefaNormal, tarefaErro, tarefaPausada:
			_ = tb.SetProgressValue(h, int(p.fracao*1000), 1000)
			_ = tb.SetProgressState(h, map[estadoTarefa]cosh.TBPF{
				tarefaNormal: cosh.TBPF_NORMAL, tarefaErro: cosh.TBPF_ERROR, tarefaPausada: cosh.TBPF_PAUSED,
			}[p.estado])
		}
	}
}

// eventoNativo recebe o HWND da janela do Gio, necessário para os diálogos,
// o arrastar e a barra de tarefas.
func (t *tela) eventoNativo(e event.Event) {
	v, ok := e.(app.Win32ViewEvent)
	if !ok || v.HWND == 0 || t.hwnd != 0 {
		return
	}
	t.hwnd = v.HWND
	instalarArrastar(v.HWND, func(caminho string) {
		select { // chamado na thread da janela: não pode esperar pela interface
		case t.fila <- func() {
			if t.sit != dividindo {
				t.definirArquivo(caminho)
			}
		}:
			t.w.Invalidate()
		default:
		}
	})
}
