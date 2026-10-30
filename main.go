// Divisor de PDF: divide PDFs enormes em arquivos menores, por número de
// páginas ou por tamanho. A mesma interface (Gio) roda no Windows e no Linux.
package main

import (
	"os"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
)

func main() {
	falhaAnterior := iniciarRegistro()
	app.ID = "divisor-pdf" // app_id no Wayland: permite regras de janela no Hyprland

	go func() {
		w := new(app.Window)
		w.Option(
			app.Title("Divisor de PDF"),
			app.Size(unit.Dp(780), unit.Dp(640)),
			app.MinSize(unit.Dp(700), unit.Dp(580)),
		)
		t := novaTela(w)
		if falhaAnterior != "" {
			t.aviso(explicarFalhaAnterior(falhaAnterior))
		}
		if len(os.Args) > 1 { // "Abrir com" ou PDF arrastado sobre o executável
			t.definirArquivo(os.Args[1])
		}
		rodar(t)
		os.Exit(0)
	}()
	app.Main()
}

func rodar(t *tela) {
	var ops op.Ops
	for {
		switch e := t.w.Event().(type) {
		case app.DestroyEvent:
			if t.cancelar != nil {
				t.cancelar()
			}
			return
		case *app.ClosingEvent:
			t.pedirFechamento(e)
		case app.ConfigEvent:
			t.focada = e.Config.Focused
		default:
			t.eventoNativo(e)
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			t.layout(gtx)
			e.Frame(gtx.Ops)
		}
	}
}
