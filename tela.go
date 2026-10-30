package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"divisorpdf/divisor"
)

type situacao int

const (
	parado situacao = iota
	dividindo
	concluido
	cancelado
	falhou
)

// estadoTarefa é o estado do progresso no botão da barra de tarefas (Windows).
type estadoTarefa int

const (
	tarefaNenhuma estadoTarefa = iota
	tarefaNormal
	tarefaErro
	tarefaPausada
)

const (
	modoPaginas = "paginas"
	modoTamanho = "tamanho"
)

type modal struct {
	titulo, texto         string
	confirmar, fechar     string // sem confirmar, só há o botão de fechar
	aoConfirmar           func()
	btConfirmar, btFechar widget.Clickable
}

type tela struct {
	w      *app.Window
	th     *material.Theme
	hwnd   uintptr
	focada bool
	fila   chan func() // ações vindas de outras goroutines, aplicadas no próximo quadro

	arquivo    string
	tamArquivo int64
	modo       widget.Enum
	edPaginas  widget.Editor
	edMB       widget.Editor
	edPasta    widget.Editor

	btZona, btPasta, btDividir, btCancelar, btAbrir, btDetalhes, btCopiar widget.Clickable
	lista                                                                 widget.List
	listaDetalhes                                                         widget.List
	btPartes                                                              []widget.Clickable
	selDetalhes                                                           widget.Selectable
	dialogoAberto                                                         bool

	sit         situacao
	prog        divisor.Progresso
	partes      []divisor.Parte
	inicio, fim time.Time
	pastaUsada  string
	cancelar    context.CancelFunc
	cancelando  bool
	erro        *erroAmigavel
	detalhes    bool
	modal       *modal
	fecharNoFim bool
}

func novaTela(w *app.Window) *tela {
	t := &tela{w: w, th: novoTema(), fila: make(chan func(), 256)}
	t.modo.Value = modoPaginas
	for _, ed := range []*widget.Editor{&t.edPaginas, &t.edMB} {
		ed.SingleLine = true
		ed.Filter = "0123456789"
		ed.MaxLen = 7
	}
	t.edPaginas.SetText("5000")
	t.edMB.SetText("500")
	t.edPasta.SingleLine = true
	t.lista.Axis = layout.Vertical
	t.listaDetalhes.Axis = layout.Vertical
	return t
}

// enviar agenda f para rodar na goroutine da interface.
func (t *tela) enviar(f func()) {
	t.fila <- f
	if t.w != nil {
		t.w.Invalidate()
	}
}

func (t *tela) aplicarFila() {
	for {
		select {
		case f := <-t.fila:
			f()
		default:
			return
		}
	}
}

func (t *tela) aviso(titulo, texto string) {
	t.modal = &modal{titulo: titulo, texto: texto, fechar: "Entendi"}
}

func (t *tela) definirArquivo(caminho string) {
	st, err := os.Stat(caminho)
	if err != nil || st.IsDir() {
		t.aviso("Arquivo não encontrado", caminho)
		return
	}
	t.arquivo, t.tamArquivo = caminho, st.Size()
	t.edPasta.SetText(filepath.Dir(caminho))
	t.sit, t.prog, t.partes, t.btPartes, t.erro = parado, divisor.Progresso{}, nil, nil, nil
	progressoTarefa(t.hwnd, tarefaNenhuma, 0)
}

// limites lê os campos do modo escolhido; dica explica o que vai acontecer
// ou o que está errado.
func (t *tela) limites() (paginas int, bytes int64, dica string, ok bool) {
	if t.modo.Value == modoPaginas {
		n, _ := strconv.Atoi(strings.TrimSpace(t.edPaginas.Text()))
		if n < 1 {
			return 0, 0, "Informe quantas páginas cada arquivo deve ter.", false
		}
		return n, 0, fmt.Sprintf("Cada arquivo terá no máximo %s %s.", milhar(n), plural(n, "página", "páginas")), true
	}
	n, _ := strconv.Atoi(strings.TrimSpace(t.edMB.Text()))
	if n < 1 {
		return 0, 0, "Informe o tamanho máximo de cada arquivo, em MB.", false
	}
	bytes = int64(n) * mb
	switch {
	case t.arquivo == "":
		dica = fmt.Sprintf("Cada arquivo terá no máximo %s MB.", milhar(n))
	case t.tamArquivo <= bytes:
		dica = fmt.Sprintf("O PDF já tem menos de %s MB: sairá um único arquivo.", milhar(n))
	default:
		partes := int((t.tamArquivo + bytes - 1) / bytes)
		dica = fmt.Sprintf("≈ %d arquivos de até %s MB cada.", partes, milhar(n))
	}
	return 0, bytes, dica, true
}

func (t *tela) pastaSaida() string {
	if p := strings.Trim(strings.TrimSpace(t.edPasta.Text()), `"`); p != "" {
		return p
	}
	return filepath.Dir(t.arquivo)
}

// iniciar confere tudo o que dá para conferir antes de gastar minutos lendo
// o PDF, e só então começa.
func (t *tela) iniciar() {
	paginas, bytes, dica, ok := t.limites()
	if !ok {
		t.aviso("Confira o limite", dica)
		return
	}
	if _, err := os.Stat(t.arquivo); err != nil {
		e := explicar(err)
		t.aviso(e.Titulo, t.arquivo)
		return
	}
	pasta := t.pastaSaida()
	if err := testarGravacao(pasta); err != nil {
		e := explicar(err)
		t.aviso("Não é possível gravar em "+pasta, e.Texto+"\n\n"+err.Error())
		return
	}
	comecar := func() { t.comecar(pasta, paginas, bytes) }
	if livre, err := espacoLivre(pasta); err == nil && livre < uint64(t.tamArquivo) {
		t.modal = &modal{
			titulo: "Pode faltar espaço em disco",
			texto: fmt.Sprintf("Juntas, as partes ocupam mais ou menos o mesmo que o PDF (%s), mas há só %s livres em %s. "+
				"Se o disco encher, a divisão para no meio.", tamanho(t.tamArquivo), tamanho(int64(livre)), pasta),
			confirmar: "Dividir mesmo assim", fechar: "Voltar", aoConfirmar: comecar,
		}
		return
	}
	comecar()
}

func testarGravacao(pasta string) error {
	if err := os.MkdirAll(pasta, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(pasta, ".divisor-teste-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

func (t *tela) comecar(pasta string, paginas int, bytes int64) {
	c, cancelar := context.WithCancel(context.Background())
	t.cancelar, t.cancelando = cancelar, false
	t.sit, t.prog, t.partes, t.btPartes = dividindo, divisor.Progresso{}, nil, nil
	t.erro, t.detalhes = nil, false
	t.inicio, t.pastaUsada = time.Now(), pasta

	o := divisor.Opcoes{Entrada: t.arquivo, PastaSaida: pasta, PaginasPorArquivo: paginas, BytesPorArquivo: bytes}
	limite := fmt.Sprintf("%d páginas", paginas)
	if bytes > 0 {
		limite = tamanho(bytes)
	}
	registrar("início: %s (%s) → %s, limite %s", t.arquivo, tamanho(t.tamArquivo), pasta, limite)
	progressoTarefa(t.hwnd, tarefaNormal, 0)
	go t.executar(c, o)
}

func (t *tela) executar(c context.Context, o divisor.Opcoes) {
	defer func() {
		if r := recover(); r != nil {
			err := &divisor.ErroInterno{Valor: r, Pilha: debug.Stack()}
			t.enviar(func() { t.terminar(c, err) })
		}
	}()
	etapa := divisor.Etapa(-1)
	o.AoProgredir = func(p divisor.Progresso) {
		if p.Etapa != etapa {
			etapa = p.Etapa
			registrar("  etapa %s após %s", rotuloEtapa(p), duracao(time.Since(t.inicio)))
		}
		t.enviar(func() {
			t.prog = p
			progressoTarefa(t.hwnd, tarefaNormal, p.Fracao)
		})
	}
	o.AoGravar = func(p divisor.Parte) {
		registrar("  gravado %s (páginas %d-%d, %s)", filepath.Base(p.Caminho), p.De, p.Ate, tamanho(p.Bytes))
		t.enviar(func() {
			t.partes = append(t.partes, p)
			t.btPartes = append(t.btPartes, widget.Clickable{})
			t.lista.Position.First, t.lista.Position.Offset = len(t.partes), 0 // mostra a parte nova
		})
	}
	_, err := divisor.Dividir(c, o)
	t.enviar(func() { t.terminar(c, err) })
}

func (t *tela) terminar(c context.Context, err error) {
	t.fim, t.cancelar = time.Now(), nil
	gasto := duracao(t.fim.Sub(t.inicio))
	var titulo, corpo string
	switch {
	case c.Err() != nil:
		t.sit = cancelado
		titulo, corpo = "Divisão cancelada", fmt.Sprintf("%d %s gravado(s) antes da interrupção.", len(t.partes), plural(len(t.partes), "arquivo", "arquivos"))
		registrar("cancelado após %s, %d partes", gasto, len(t.partes))
		progressoTarefa(t.hwnd, tarefaPausada, t.prog.Fracao)
	case err != nil:
		t.sit = falhou
		e := explicar(err)
		t.erro = &e
		titulo, corpo = e.Titulo, e.Texto
		registrar("ERRO após %s: %s\n%s", gasto, e.Titulo, e.Tecnico)
		progressoTarefa(t.hwnd, tarefaErro, max(t.prog.Fracao, 0.05))
	default:
		t.sit = concluido
		titulo = "Divisão concluída"
		corpo = fmt.Sprintf("%d %s em %s.", len(t.partes), plural(len(t.partes), "arquivo gravado", "arquivos gravados"), gasto)
		registrar("concluído em %s, %d partes", gasto, len(t.partes))
		progressoTarefa(t.hwnd, tarefaNenhuma, 0)
	}
	if !t.focada {
		notificar(t.hwnd, titulo, corpo)
	}
	if t.fecharNoFim {
		t.w.Perform(system.ActionClose)
	}
}

func (t *tela) pedirFechamento(e *app.ClosingEvent) {
	if t.sit != dividindo {
		return
	}
	e.Abort()
	if t.fecharNoFim {
		return
	}
	t.modal = &modal{
		titulo:    "A divisão ainda está em andamento",
		texto:     "Deseja interromper e fechar? Os arquivos já gravados serão mantidos.",
		confirmar: "Interromper e fechar", fechar: "Continuar dividindo",
		aoConfirmar: func() {
			t.fecharNoFim, t.cancelando = true, true
			t.cancelar()
		},
	}
}

func rotuloEtapa(p divisor.Progresso) string {
	switch p.Etapa {
	case divisor.Lendo:
		return "Lendo o PDF…"
	case divisor.Verificando:
		return "Verificando a estrutura…"
	case divisor.Preparando:
		return "Organizando as páginas…"
	default:
		if p.Parte > 0 {
			return fmt.Sprintf("Gravando parte %d…", p.Parte)
		}
		return "Gravando…"
	}
}

// eventos trata os cliques do quadro antes de desenhá-lo.
func (t *tela) eventos(gtx C) {
	livre := t.sit != dividindo && !t.dialogoAberto
	if gtx.Source.Focused(&t.edMB) {
		t.modo.Value = modoTamanho
	}
	if gtx.Source.Focused(&t.edPaginas) {
		t.modo.Value = modoPaginas
	}

	if t.btZona.Clicked(gtx) && livre {
		t.dialogoAberto = true
		atual := t.arquivo
		go func() {
			caminho, ok, err := escolherArquivo(t.hwnd, atual)
			t.enviar(func() {
				t.dialogoAberto = false
				switch {
				case err != nil:
					t.aviso("Não foi possível abrir o seletor de arquivos", err.Error())
				case ok:
					t.definirArquivo(caminho)
				}
			})
		}()
	}
	if t.btPasta.Clicked(gtx) && livre {
		t.dialogoAberto = true
		atual := t.pastaSaida()
		go func() {
			caminho, ok, err := escolherPasta(t.hwnd, atual)
			t.enviar(func() {
				t.dialogoAberto = false
				switch {
				case err != nil:
					t.aviso("Não foi possível abrir o seletor de pastas", err.Error())
				case ok:
					t.edPasta.SetText(caminho)
				}
			})
		}()
	}
	if t.btDividir.Clicked(gtx) && livre && t.arquivo != "" {
		t.iniciar()
	}
	if t.btCancelar.Clicked(gtx) && t.cancelar != nil && !t.cancelando {
		t.cancelando = true
		t.cancelar()
	}
	if t.btAbrir.Clicked(gtx) && t.pastaUsada != "" {
		abrirPasta(t.pastaUsada)
	}
	if t.btDetalhes.Clicked(gtx) {
		t.detalhes = !t.detalhes
	}
	if t.btCopiar.Clicked(gtx) && t.erro != nil {
		gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(t.textoDetalhes()))})
	}
	for i := range t.btPartes {
		if t.btPartes[i].Clicked(gtx) {
			mostrarArquivo(t.partes[i].Caminho)
		}
	}
}

func (t *tela) textoDetalhes() string {
	s := t.erro.Tecnico
	if r := caminhoRegistro(); r != "" {
		s += "\n\nRegistro completo: " + r
	}
	return s
}

func (t *tela) layout(gtx C) D {
	t.aplicarFila()
	if t.sit == dividindo {
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(500 * time.Millisecond)}) // o tempo decorrido anda sozinho
	}
	preencherFundo(gtx, corFundo)

	fundo := gtx
	if t.modal != nil {
		fundo = gtx.Disabled()
	}
	t.eventos(fundo)
	d := layout.Inset{Top: 20, Bottom: 20, Left: 24, Right: 24}.Layout(fundo, func(gtx C) D {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(t.cabecalho),
			layout.Rigid(layout.Spacer{Height: 14}.Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.Y = gtx.Dp(158)
				gtx.Constraints.Max.Y = gtx.Constraints.Min.Y
				return layout.Flex{}.Layout(gtx,
					layout.Flexed(0.46, t.zona),
					layout.Rigid(layout.Spacer{Width: 12}.Layout),
					layout.Flexed(0.54, t.opcoes),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Rigid(t.salvarEm),
			layout.Rigid(layout.Spacer{Height: 12}.Layout),
			layout.Flexed(1, t.andamento),
			layout.Rigid(layout.Spacer{Height: 16}.Layout),
			layout.Rigid(t.botoes),
		)
	})
	if t.modal != nil {
		t.desenharModal(gtx)
	}
	return d
}

func (t *tela) cabecalho(gtx C) D {
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(marca),
		layout.Rigid(layout.Spacer{Width: 14}.Layout),
		layout.Flexed(1, func(gtx C) D {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Rigid(texto(t.th, 22, corTexto, font.SemiBold, "Divisor de PDF").Layout),
				layout.Rigid(texto(t.th, 13, corSecundaria, font.Normal, "Divida PDFs enormes em arquivos menores, por páginas ou por tamanho.").Layout),
			)
		}),
	)
}

func (t *tela) zona(gtx C) D {
	ativo := t.sit != dividindo
	if !ativo {
		gtx = gtx.Disabled()
	}
	return t.btZona.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min = gtx.Constraints.Max
		r := image.Rectangle{Max: gtx.Constraints.Max}
		passando := ativo && t.btZona.Hovered()
		raio := gtx.Dp(8)
		fundo, borda := corCartao, rgb(0xC4C4C4)
		switch {
		case passando:
			fundo, borda = corAcentoFundo, corAcento
		case t.arquivo != "":
			borda = rgb(0xA9C8E6)
		}
		retangulo(gtx, r, raio, fundo)
		tracejado(gtx, r, float32(raio), float32(gtx.Dp(6)), float32(gtx.Dp(4)), float32(gtx.Dp(1.5)), borda)

		var d D
		if t.arquivo == "" {
			d = layout.Center.Layout(gtx, func(gtx C) D {
				titulo, sub := "Arraste o PDF aqui", "ou clique para escolher um arquivo"
				if !suportaArrastar {
					titulo, sub = "Clique para escolher o PDF", "Funciona com arquivos de qualquer tamanho"
				}
				return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return iconePDF(gtx, t.th, 46) }),
					layout.Rigid(layout.Spacer{Height: 12}.Layout),
					layout.Rigid(texto(t.th, 15, corTexto, font.SemiBold, titulo).Layout),
					layout.Rigid(layout.Spacer{Height: 2}.Layout),
					layout.Rigid(texto(t.th, 13, corSecundaria, font.Normal, sub).Layout),
				)
			})
		} else {
			d = layout.UniformInset(20).Layout(gtx, func(gtx C) D {
				return layout.W.Layout(gtx, func(gtx C) D {
					return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
						layout.Rigid(func(gtx C) D { return iconePDF(gtx, t.th, 56) }),
						layout.Rigid(layout.Spacer{Width: 18}.Layout),
						layout.Flexed(1, func(gtx C) D {
							troca := "Clique para trocar de arquivo"
							if suportaArrastar {
								troca = "Clique ou arraste outro PDF para trocar"
							}
							return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
								layout.Rigid(func(gtx C) D {
									l := texto(t.th, 15, corTexto, font.SemiBold, filepath.Base(t.arquivo))
									l.MaxLines = 2
									return l.Layout(gtx)
								}),
								layout.Rigid(layout.Spacer{Height: 3}.Layout),
								layout.Rigid(texto(t.th, 13, corSecundaria, font.Normal, tamanho(t.tamArquivo)).Layout),
								layout.Rigid(layout.Spacer{Height: 1}.Layout),
								layout.Rigid(texto(t.th, 12, corTerciaria, font.Normal, filepath.Dir(t.arquivo)).Layout),
								layout.Rigid(layout.Spacer{Height: 10}.Layout),
								layout.Rigid(texto(t.th, 12, corAcento, font.Medium, troca).Layout),
							)
						}),
					)
				})
			})
		}
		if ativo {
			cursorMao(gtx, D{Size: r.Max})
		}
		return D{Size: r.Max, Baseline: d.Baseline}
	})
}

func (t *tela) opcoes(gtx C) D {
	ocupado := t.sit == dividindo
	if ocupado {
		gtx = gtx.Disabled()
	}
	gtx.Constraints.Min = gtx.Constraints.Max
	_, _, dica, ok := t.limites()
	linha := func(chave, rotulo string, ed *widget.Editor, unidade string) layout.Widget {
		return func(gtx C) D {
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return radio(gtx, t.th, &t.modo, chave, rotulo, 92) }),
				layout.Rigid(func(gtx C) D {
					return campo(gtx, t.th, ed, 78, "", !ok && t.modo.Value == chave)
				}),
				layout.Rigid(layout.Spacer{Width: 8}.Layout),
				layout.Flexed(1, texto(t.th, 13, corSecundaria, font.Normal, unidade).Layout),
			)
		}
	}
	return cartaoCom(gtx, layout.Inset{Top: 14, Bottom: 12, Left: 16, Right: 16}, func(gtx C) D {
		corDica := corSecundaria
		if !ok {
			corDica = corErro
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(texto(t.th, 14, corTexto, font.SemiBold, "Como dividir").Layout),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(linha(modoPaginas, "Por páginas", &t.edPaginas, "páginas por arquivo")),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(linha(modoTamanho, "Por tamanho", &t.edMB, "MB por arquivo")),
			layout.Flexed(1, func(gtx C) D {
				return layout.S.Layout(gtx, func(gtx C) D {
					l := paragrafo(t.th, 12.5, corDica, dica)
					l.MaxLines = 2
					return l.Layout(gtx)
				})
			}),
		)
	})
}

func (t *tela) salvarEm(gtx C) D {
	ocupado := t.sit == dividindo
	if ocupado {
		gtx = gtx.Disabled()
	}
	return cartaoCom(gtx, layout.Inset{Top: 10, Bottom: 10, Left: 16, Right: 10}, func(gtx C) D {
		return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(84)
				return texto(t.th, 14, corTexto, font.SemiBold, "Salvar em").Layout(gtx)
			}),
			layout.Flexed(1, func(gtx C) D { return campo(gtx, t.th, &t.edPasta, 0, "A mesma pasta do PDF", false) }),
			layout.Rigid(layout.Spacer{Width: 8}.Layout),
			layout.Rigid(func(gtx C) D {
				return botao(gtx, t.th, &t.btPasta, "Alterar…", botaoSecundario, !ocupado && !t.dialogoAberto)
			}),
		)
	})
}

// situacaoTexto devolve a linha de status, sua cor e a linha de detalhes.
func (t *tela) situacaoTexto(agora time.Time) (status string, cor color.NRGBA, detalhe string) {
	n := len(t.partes)
	arquivos := fmt.Sprintf("%d %s", n, plural(n, "arquivo", "arquivos"))
	switch t.sit {
	case dividindo:
		status, cor = rotuloEtapa(t.prog), corTexto
		if t.cancelando {
			status = "Cancelando…"
		}
		var partes []string
		switch {
		case t.prog.Etapa == divisor.Gravando && t.prog.De > 0:
			partes = append(partes, fmt.Sprintf("Páginas %s a %s de %s", milhar(t.prog.De), milhar(t.prog.Ate), milhar(t.prog.Paginas)))
		case t.prog.Paginas > 0:
			partes = append(partes, milhar(t.prog.Paginas)+" páginas")
		default:
			partes = append(partes, tamanho(t.tamArquivo))
		}
		decorrido := agora.Sub(t.inicio)
		partes = append(partes, duracao(decorrido)+" decorridos")
		if f := t.prog.Fracao; f >= 0.1 && decorrido >= 3*time.Second && f < 1 {
			resta := time.Duration(float64(decorrido) * (1 - f) / f)
			if resta < 5*time.Second {
				partes = append(partes, "quase lá")
			} else {
				partes = append(partes, "cerca de "+duracao(resta)+" restantes")
			}
		}
		detalhe = strings.Join(partes, "  ·  ")
	case concluido:
		var total int64
		for _, p := range t.partes {
			total += p.Bytes
		}
		status, cor = fmt.Sprintf("Concluído! %s %s", arquivos, plural(n, "gravado", "gravados")), corSucesso
		detalhe = fmt.Sprintf("%s no total  ·  feito em %s  ·  clique num arquivo para mostrá-lo na pasta", tamanho(total), duracao(t.fim.Sub(t.inicio)))
	case cancelado:
		status, cor = "Divisão cancelada", corAlerta
		detalhe = fmt.Sprintf("%s %s antes da interrupção  ·  %s", arquivos, plural(n, "gravado", "gravados"), duracao(t.fim.Sub(t.inicio)))
	case falhou:
		status, cor = t.erro.Titulo, corErro
		detalhe = fmt.Sprintf("Parou depois de %s  ·  %s %s antes do erro", duracao(t.fim.Sub(t.inicio)), arquivos, plural(n, "gravado", "gravados"))
	default:
		if t.arquivo == "" {
			status, cor = "Escolha um PDF para começar", corSecundaria
			detalhe = "Clique na área acima para escolher o arquivo."
			if suportaArrastar {
				detalhe = "Arraste o arquivo para a área acima ou clique nela."
			}
		} else {
			status, cor = "Pronto para dividir", corTexto
			detalhe = "Confira as opções e clique em Dividir PDF."
		}
	}
	return status, cor, detalhe
}

func (t *tela) andamento(gtx C) D {
	gtx.Constraints.Min = gtx.Constraints.Max
	status, corStatus, detalhe := t.situacaoTexto(gtx.Now)
	fracao, corBarra := float32(t.prog.Fracao), corAcento
	switch t.sit {
	case concluido:
		fracao, corBarra = 1, corSucesso
	case falhou:
		corBarra = corErro
	case cancelado:
		corBarra = corAlerta
	}

	return cartao(gtx, func(gtx C) D {
		filhos := []layout.FlexChild{
			layout.Rigid(func(gtx C) D {
				return layout.Flex{Alignment: layout.Baseline}.Layout(gtx,
					layout.Flexed(1, texto(t.th, 16, corStatus, font.SemiBold, status).Layout),
					layout.Rigid(func(gtx C) D {
						if t.sit != dividindo {
							return D{}
						}
						return texto(t.th, 16, corSecundaria, font.SemiBold, fmt.Sprintf("%d%%", int(fracao*100))).Layout(gtx)
					}),
				)
			}),
			layout.Rigid(layout.Spacer{Height: 10}.Layout),
			layout.Rigid(func(gtx C) D { return barraProgresso(gtx, fracao, corBarra) }),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(texto(t.th, 12.5, corSecundaria, font.Normal, detalhe).Layout),
		}
		if t.erro != nil {
			filhos = append(filhos,
				layout.Rigid(layout.Spacer{Height: 12}.Layout),
				layout.Rigid(t.caixaErro),
			)
		}
		if excedem := t.contarExcede(); excedem > 0 {
			filhos = append(filhos,
				layout.Rigid(layout.Spacer{Height: 8}.Layout),
				layout.Rigid(func(gtx C) D {
					_, bytes, _, _ := t.limites()
					s := fmt.Sprintf("%d %s acima de %s: %s uma página maior que o limite, e páginas não podem ser partidas.",
						excedem, plural(excedem, "arquivo ficou", "arquivos ficaram"), tamanho(bytes), plural(excedem, "ele contém", "cada um contém"))
					return faixa(gtx, t.th, s, corAlerta, corAlertaFundo)
				}),
			)
		}
		filhos = append(filhos,
			layout.Rigid(layout.Spacer{Height: 14}.Layout),
			layout.Flexed(1, t.areaInferior),
		)
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, filhos...)
	})
}

func (t *tela) contarExcede() int {
	n := 0
	for _, p := range t.partes {
		if p.Excede {
			n++
		}
	}
	return n
}

// faixa é uma mensagem destacada com fundo colorido.
func faixa(gtx C, th *material.Theme, s string, cor, fundo color.NRGBA) D {
	m := op.Record(gtx.Ops)
	d := layout.Inset{Top: 6, Bottom: 6, Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return paragrafo(th, 12.5, cor, s).Layout(gtx)
	})
	call := m.Stop()
	retangulo(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(6), fundo)
	call.Add(gtx.Ops)
	return d
}

func (t *tela) caixaErro(gtx C) D {
	m := op.Record(gtx.Ops)
	d := layout.Inset{Top: 10, Bottom: 10, Left: 12, Right: 12}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(paragrafo(t.th, 13, corTexto, t.erro.Texto).Layout),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx C) D {
				rotulo := "Mostrar detalhes técnicos"
				if t.detalhes {
					rotulo = "Ocultar detalhes técnicos"
				}
				return layout.Flex{}.Layout(gtx,
					layout.Rigid(func(gtx C) D { return link(gtx, t.th, &t.btDetalhes, rotulo) }),
					layout.Rigid(layout.Spacer{Width: 16}.Layout),
					layout.Rigid(func(gtx C) D { return link(gtx, t.th, &t.btCopiar, "Copiar detalhes") }),
				)
			}),
		)
	})
	call := m.Stop()
	retangulo(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(6), corErroFundo)
	call.Add(gtx.Ops)
	return d
}

func link(gtx C, th *material.Theme, cl *widget.Clickable, s string) D {
	return cl.Layout(gtx, func(gtx C) D {
		cor := corAcento
		if cl.Hovered() {
			cor = rgb(0x003E92)
		}
		d := texto(th, 12.5, cor, font.Medium, s).Layout(gtx)
		cursorMao(gtx, d)
		return d
	})
}

func (t *tela) areaInferior(gtx C) D {
	if t.erro != nil && t.detalhes {
		return t.detalhesTecnicos(gtx)
	}
	n := len(t.partes)
	return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			s := "ARQUIVOS GERADOS"
			if n > 0 {
				s += fmt.Sprintf("  ·  %d", n)
			}
			return texto(t.th, 11, corTerciaria, font.SemiBold, s).Layout(gtx)
		}),
		layout.Rigid(layout.Spacer{Height: 6}.Layout),
		layout.Flexed(1, func(gtx C) D {
			if n == 0 {
				vazio := "Os arquivos gerados aparecem aqui."
				if t.sit == dividindo {
					vazio = "O primeiro arquivo aparece aqui assim que for gravado."
				}
				return layout.Center.Layout(gtx, texto(t.th, 13, corTerciaria, font.Normal, vazio).Layout)
			}
			return material.List(t.th, &t.lista).Layout(gtx, n, t.linhaParte)
		}),
	)
}

func (t *tela) linhaParte(gtx C, i int) D {
	p := t.partes[i]
	cl := &t.btPartes[i]
	return cl.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		d := layout.Inset{Top: 6, Bottom: 6, Left: 8, Right: 10}.Layout(gtx, func(gtx C) D {
			gtx.Constraints.Min.X = gtx.Constraints.Max.X
			corTam := corTexto
			if p.Excede {
				corTam = corAlerta
			}
			return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx C) D { return iconePDF(gtx, t.th, 26) }),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Flexed(1, func(gtx C) D {
					return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
						layout.Rigid(texto(t.th, 13, corTexto, font.Normal, filepath.Base(p.Caminho)).Layout),
						layout.Rigid(texto(t.th, 12, corSecundaria, font.Normal,
							fmt.Sprintf("Páginas %s a %s  ·  %s %s", milhar(p.De), milhar(p.Ate), milhar(p.Ate-p.De+1), plural(p.Ate-p.De+1, "página", "páginas"))).Layout),
					)
				}),
				layout.Rigid(layout.Spacer{Width: 12}.Layout),
				layout.Rigid(func(gtx C) D {
					if !p.Excede {
						return D{}
					}
					return layout.Inset{Right: 10}.Layout(gtx, func(gtx C) D {
						return selo(gtx, t.th, "acima do limite", corAlerta, corAlertaFundo)
					})
				}),
				layout.Rigid(func(gtx C) D {
					gtx.Constraints.Min.X = gtx.Dp(76)
					l := texto(t.th, 13, corTam, font.Medium, tamanho(p.Bytes))
					l.Alignment = text.End
					return l.Layout(gtx)
				}),
			)
		})
		call := m.Stop()
		if cl.Hovered() {
			retangulo(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(4), corHover)
		}
		call.Add(gtx.Ops)
		cursorMao(gtx, d)
		return d
	})
}

func (t *tela) detalhesTecnicos(gtx C) D {
	gtx.Constraints.Min = gtx.Constraints.Max
	r := image.Rectangle{Max: gtx.Constraints.Max}
	retangulo(gtx, r, gtx.Dp(6), rgb(0xF7F7F7))
	contorno(gtx, r, gtx.Dp(6), float32(gtx.Dp(1)), corBorda)
	return layout.UniformInset(10).Layout(gtx, func(gtx C) D {
		return material.List(t.th, &t.listaDetalhes).Layout(gtx, 1, func(gtx C, _ int) D {
			l := paragrafo(t.th, 12, corSecundaria, t.textoDetalhes())
			l.Font.Typeface = "Cascadia Mono, Consolas, JetBrainsMono Nerd Font, DejaVu Sans Mono, Go Mono"
			l.State = &t.selDetalhes
			return l.Layout(gtx)
		})
	})
}

func (t *tela) botoes(gtx C) D {
	ocupado := t.sit == dividindo
	_, _, _, ok := t.limites()
	return layout.Flex{Alignment: layout.Middle}.Layout(gtx,
		layout.Rigid(func(gtx C) D {
			return botao(gtx, t.th, &t.btAbrir, "Abrir pasta", botaoSecundario, !ocupado && len(t.partes) > 0)
		}),
		layout.Flexed(1, layout.Spacer{}.Layout),
		layout.Rigid(func(gtx C) D {
			return botao(gtx, t.th, &t.btCancelar, "Cancelar", botaoSecundario, ocupado && !t.cancelando)
		}),
		layout.Rigid(layout.Spacer{Width: 8}.Layout),
		layout.Rigid(func(gtx C) D {
			return botao(gtx, t.th, &t.btDividir, "Dividir PDF", botaoPrimario, !ocupado && t.arquivo != "" && ok && !t.dialogoAberto)
		}),
	)
}

func (t *tela) desenharModal(gtx C) {
	m := t.modal
	if m.btFechar.Clicked(gtx) {
		t.modal = nil
		return
	}
	if m.btConfirmar.Clicked(gtx) {
		t.modal = nil
		if m.aoConfirmar != nil {
			m.aoConfirmar()
		}
		return
	}

	// Por trás do véu a tela foi desenhada desabilitada, então não recebe cliques.
	paint.FillShape(gtx.Ops, color.NRGBA{A: 0x70}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	layout.Center.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(460))
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		return cartao(gtx, func(gtx C) D {
			return layout.UniformInset(8).Layout(gtx, func(gtx C) D {
				return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
					layout.Rigid(func(gtx C) D {
						l := paragrafo(t.th, 17, corTexto, m.titulo)
						l.Font.Weight = font.SemiBold
						return l.Layout(gtx)
					}),
					layout.Rigid(layout.Spacer{Height: 10}.Layout),
					layout.Rigid(paragrafo(t.th, 14, corSecundaria, m.texto).Layout),
					layout.Rigid(layout.Spacer{Height: 22}.Layout),
					layout.Rigid(func(gtx C) D {
						return layout.Flex{}.Layout(gtx,
							layout.Flexed(1, layout.Spacer{}.Layout),
							layout.Rigid(func(gtx C) D {
								tipo := botaoSecundario
								if m.confirmar == "" {
									tipo = botaoPrimario
								}
								return botao(gtx, t.th, &m.btFechar, m.fechar, tipo, true)
							}),
							layout.Rigid(func(gtx C) D {
								if m.confirmar == "" {
									return D{}
								}
								return layout.Inset{Left: 8}.Layout(gtx, func(gtx C) D {
									return botao(gtx, t.th, &m.btConfirmar, m.confirmar, botaoPrimario, true)
								})
							}),
						)
					}),
				)
			})
		})
	})
}
