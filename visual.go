package main

import (
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
)

// Paleta inspirada no Windows 11 (Fluent).
var (
	corFundo        = rgb(0xF3F3F3)
	corCartao       = rgb(0xFFFFFF)
	corBorda        = rgb(0xE5E5E5)
	corBordaCampo   = rgb(0xD1D1D1)
	corTexto        = rgb(0x1B1B1B)
	corSecundaria   = rgb(0x616161)
	corTerciaria    = rgb(0x8A8A8A)
	corAcento       = rgb(0x0067C0)
	corAcentoHover  = rgb(0x1975C5)
	corAcentoFundo  = rgb(0xEFF6FC)
	corSucesso      = rgb(0x0F7B0F)
	corSucessoFundo = rgb(0xDFF6DD)
	corErro         = rgb(0xC42B1C)
	corErroFundo    = rgb(0xFDE7E9)
	corAlerta       = rgb(0x9D5D00)
	corAlertaFundo  = rgb(0xFFF4CE)
	corTrilho       = rgb(0xE3E3E3)
	corPDF          = rgb(0xD13438)
	corHover        = rgb(0xF6F6F6)
	corDesabilitado = rgb(0xBDBDBD)
)

func rgb(c uint32) color.NRGBA {
	return color.NRGBA{R: uint8(c >> 16), G: uint8(c >> 8), B: uint8(c), A: 0xFF}
}

func novoTema() *material.Theme {
	th := material.NewTheme()
	// As fontes do sistema (Segoe UI no Windows; Noto Sans ou Inter no Linux)
	// são carregadas sob demanda; as Go Fonts ficam de reserva.
	th.Shaper = text.NewShaper(text.WithCollection(gofont.Collection()))
	// Fontes variáveis (Segoe UI Variable, Adwaita Sans) ficam de fora: o Gio
	// só usa o peso padrão delas e os títulos perderiam o seminegrito.
	th.Face = "Segoe UI, Noto Sans, Inter, Cantarell, Liberation Sans, DejaVu Sans, Go"
	th.Palette = material.Palette{Bg: corCartao, Fg: corTexto, ContrastBg: corAcento, ContrastFg: corCartao}
	th.TextSize = 14
	return th
}

type (
	C = layout.Context
	D = layout.Dimensions
)

// texto é um rótulo de uma linha, truncado com reticências se não couber.
func texto(th *material.Theme, tam unit.Sp, cor color.NRGBA, peso font.Weight, s string) material.LabelStyle {
	l := material.Label(th, tam, s)
	l.Color = cor
	l.Font.Weight = peso
	l.MaxLines = 1
	l.Truncator = "…"
	return l
}

// paragrafo quebra linhas.
func paragrafo(th *material.Theme, tam unit.Sp, cor color.NRGBA, s string) material.LabelStyle {
	l := material.Label(th, tam, s)
	l.Color = cor
	return l
}

func retangulo(gtx C, r image.Rectangle, raio int, cor color.NRGBA) {
	paint.FillShape(gtx.Ops, cor, clip.UniformRRect(r, raio).Op(gtx.Ops))
}

func contorno(gtx C, r image.Rectangle, raio int, largura float32, cor color.NRGBA) {
	rr := clip.UniformRRect(r, raio)
	paint.FillShape(gtx.Ops, cor, clip.Stroke{Path: rr.Path(gtx.Ops), Width: largura}.Op())
}

// cartao desenha o fundo branco com borda sutil por trás do conteúdo.
func cartao(gtx C, conteudo layout.Widget) D {
	return cartaoCom(gtx, layout.UniformInset(16), conteudo)
}

func cartaoCom(gtx C, margem layout.Inset, conteudo layout.Widget) D {
	m := op.Record(gtx.Ops)
	d := margem.Layout(gtx, conteudo)
	call := m.Stop()
	r := image.Rectangle{Max: d.Size}
	raio := gtx.Dp(8)
	retangulo(gtx, r, raio, corBorda)
	retangulo(gtx, r.Inset(gtx.Dp(1)), raio-gtx.Dp(1), corCartao)
	call.Add(gtx.Ops)
	return d
}

// cursorMao mostra a mãozinha sobre a área já desenhada.
func cursorMao(gtx C, d D) {
	defer clip.Rect{Max: d.Size}.Push(gtx.Ops).Pop()
	pointer.CursorPointer.Add(gtx.Ops)
}

// tracejado desenha um retângulo arredondado com borda tracejada, seguindo
// o perímetro (o Gio não tem traço pontilhado próprio).
func tracejado(gtx C, r image.Rectangle, raio, traco, vao float32, largura float32, cor color.NRGBA) {
	x0, y0 := float32(r.Min.X)+largura/2, float32(r.Min.Y)+largura/2
	x1, y1 := float32(r.Max.X)-largura/2, float32(r.Max.Y)-largura/2
	var pts []f32.Point
	canto := func(cx, cy float32, ini float64) {
		for i := 0; i <= 8; i++ {
			a := ini + float64(i)*math.Pi/2/8
			pts = append(pts, f32.Pt(cx+raio*float32(math.Cos(a)), cy+raio*float32(math.Sin(a))))
		}
	}
	canto(x1-raio, y0+raio, -math.Pi/2)
	canto(x1-raio, y1-raio, 0)
	canto(x0+raio, y1-raio, math.Pi/2)
	canto(x0+raio, y0+raio, math.Pi)
	pts = append(pts, pts[0])

	var p clip.Path
	p.Begin(gtx.Ops)
	desenhando, resta := true, traco
	p.MoveTo(pts[0])
	for i := 1; i < len(pts); i++ {
		a, b := pts[i-1], pts[i]
		seg := b.Sub(a)
		comp := float32(math.Hypot(float64(seg.X), float64(seg.Y)))
		pos := float32(0)
		for comp-pos > resta {
			pos += resta
			q := a.Add(seg.Mul(pos / comp))
			if desenhando {
				p.LineTo(q)
				resta = vao
			} else {
				p.MoveTo(q)
				resta = traco
			}
			desenhando = !desenhando
		}
		resta -= comp - pos
		if desenhando {
			p.LineTo(b)
		} else {
			p.MoveTo(b)
		}
	}
	paint.FillShape(gtx.Ops, cor, clip.Stroke{Path: p.End(), Width: largura}.Op())
}

// iconePDF desenha uma folha com o canto dobrado e a etiqueta vermelha "PDF".
// alt é a altura em dp; abaixo de ~28 dp a etiqueta fica sem letras.
func iconePDF(gtx C, th *material.Theme, alt unit.Dp) D {
	h := float32(gtx.Dp(alt))
	w := h * 0.78
	dobra := w * 0.32
	l := float32(gtx.Dp(1))
	sz := image.Pt(int(w+0.5)+gtx.Dp(2), int(h+0.5))

	folha := func() clip.PathSpec {
		var p clip.Path
		p.Begin(gtx.Ops)
		p.MoveTo(f32.Pt(l, l))
		p.LineTo(f32.Pt(w-dobra, l))
		p.LineTo(f32.Pt(w-l, dobra))
		p.LineTo(f32.Pt(w-l, h-l))
		p.LineTo(f32.Pt(l, h-l))
		p.Close()
		return p.End()
	}
	paint.FillShape(gtx.Ops, corCartao, clip.Outline{Path: folha()}.Op())
	paint.FillShape(gtx.Ops, rgb(0x9A9A9A), clip.Stroke{Path: folha(), Width: l * 1.2}.Op())

	var d clip.Path
	d.Begin(gtx.Ops)
	d.MoveTo(f32.Pt(w-dobra, l))
	d.LineTo(f32.Pt(w-dobra, dobra))
	d.LineTo(f32.Pt(w-l, dobra))
	d.Close()
	paint.FillShape(gtx.Ops, rgb(0xE4E4E4), clip.Outline{Path: d.End()}.Op())

	// linhas de "texto" da folha
	for i := range 3 {
		y := int(h*0.30) + i*int(h*0.1)
		fim := int(w * 0.72)
		if i == 0 {
			fim = int(w * 0.5)
		}
		retangulo(gtx, image.Rect(int(w*0.2), y, fim, y+max(1, int(h*0.035))), 1, rgb(0xC8C8C8))
	}

	et := image.Rect(-int(w*0.12), int(h*0.58), int(w*0.74), int(h*0.86))
	retangulo(gtx, et, gtx.Dp(2), corPDF)
	if alt >= 28 {
		rot := texto(th, unit.Sp(float32(alt)*0.2), corCartao, font.Bold, "PDF")
		st := op.Offset(et.Min).Push(gtx.Ops)
		g := gtx
		g.Constraints = layout.Exact(et.Size())
		layout.Center.Layout(g, rot.Layout)
		st.Pop()
	}
	return D{Size: sz}
}

// marca é o ícone do aplicativo no cabeçalho: uma folha cortada ao meio.
func marca(gtx C) D {
	s := gtx.Dp(40)
	retangulo(gtx, image.Rect(0, 0, s, s), gtx.Dp(10), corAcento)
	u := float32(s) / 40
	var p clip.Path
	p.Begin(gtx.Ops)
	p.MoveTo(f32.Pt(13*u, 9*u))
	p.LineTo(f32.Pt(23*u, 9*u))
	p.LineTo(f32.Pt(28*u, 14*u))
	p.LineTo(f32.Pt(28*u, 31*u))
	p.LineTo(f32.Pt(13*u, 31*u))
	p.Close()
	paint.FillShape(gtx.Ops, corCartao, clip.Stroke{Path: p.End(), Width: 2 * u}.Op())
	// linha de corte tracejada atravessando a folha
	for x := float32(8); x < 33; x += 5 {
		retangulo(gtx, image.Rect(int(x*u), int(19.5*u), int((x+3)*u), int(21.5*u)), 0, corCartao)
	}
	return D{Size: image.Pt(s, s)}
}

type tipoBotao int

const (
	botaoSecundario tipoBotao = iota
	botaoPrimario
)

func botao(gtx C, th *material.Theme, cl *widget.Clickable, rotulo string, tipo tipoBotao, ativo bool) D {
	if !ativo {
		gtx = gtx.Disabled()
	}
	return cl.Layout(gtx, func(gtx C) D {
		m := op.Record(gtx.Ops)
		cor := corTexto
		if tipo == botaoPrimario {
			cor = corCartao
		}
		if !ativo && tipo == botaoSecundario {
			cor = corDesabilitado
		}
		gtx.Constraints.Min.Y = gtx.Dp(34)
		gtx.Constraints.Min.X = max(gtx.Constraints.Min.X, gtx.Dp(96))
		d := layout.Inset{Left: 18, Right: 18}.Layout(gtx, func(gtx C) D {
			return layout.Center.Layout(gtx, texto(th, 14, cor, font.Medium, rotulo).Layout)
		})
		call := m.Stop()

		r := image.Rectangle{Max: d.Size}
		raio := gtx.Dp(6)
		switch {
		case tipo == botaoPrimario && !ativo:
			retangulo(gtx, r, raio, corDesabilitado)
		case tipo == botaoPrimario:
			fundo := corAcento
			if cl.Hovered() {
				fundo = corAcentoHover
			}
			if cl.Pressed() {
				fundo = rgb(0x005A9E)
			}
			retangulo(gtx, r, raio, fundo)
		default:
			retangulo(gtx, r, raio, corBordaCampo)
			fundo := corCartao
			if ativo && cl.Hovered() {
				fundo = rgb(0xF9F9F9)
			}
			if ativo && cl.Pressed() {
				fundo = rgb(0xF0F0F0)
			}
			retangulo(gtx, r.Inset(gtx.Dp(1)), raio-gtx.Dp(1), fundo)
		}
		call.Add(gtx.Ops)
		if ativo {
			cursorMao(gtx, d)
		}
		return d
	})
}

// radio desenha uma opção no estilo Fluent: círculo azul com miolo branco.
func radio(gtx C, th *material.Theme, en *widget.Enum, chave, rotulo string, largura unit.Dp) D {
	return en.Layout(gtx, chave, func(gtx C) D {
		marcado := en.Value == chave
		passando, _ := en.Hovered()
		d := layout.Flex{Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx C) D {
				s := gtx.Dp(20)
				r := image.Rect(0, 0, s, s)
				if marcado {
					retangulo(gtx, r, s/2, corAcento)
					m := gtx.Dp(5)
					if passando == chave {
						m = gtx.Dp(4)
					}
					miolo := r.Inset(m)
					retangulo(gtx, miolo, miolo.Dx()/2, corCartao)
				} else {
					borda := corSecundaria
					if passando == chave {
						borda = corTexto
					}
					retangulo(gtx, r, s/2, borda)
					retangulo(gtx, r.Inset(gtx.Dp(1)), s/2, corCartao)
				}
				return D{Size: image.Pt(s, s)}
			}),
			layout.Rigid(layout.Spacer{Width: 10}.Layout),
			layout.Rigid(func(gtx C) D {
				gtx.Constraints.Min.X = gtx.Dp(largura)
				gtx.Constraints.Max.X = gtx.Dp(largura)
				return texto(th, 14, corTexto, font.Normal, rotulo).Layout(gtx)
			}),
		)
		cursorMao(gtx, d)
		return d
	})
}

// campo é uma caixa de texto Fluent: borda clara e linha inferior azul com foco.
func campo(gtx C, th *material.Theme, ed *widget.Editor, largura unit.Dp, dica string, invalido bool) D {
	if largura > 0 {
		gtx.Constraints.Min.X = gtx.Dp(largura)
		gtx.Constraints.Max.X = gtx.Dp(largura)
	}
	foco := gtx.Source.Focused(ed)
	m := op.Record(gtx.Ops)
	d := layout.Inset{Top: 6, Bottom: 6, Left: 10, Right: 10}.Layout(gtx, func(gtx C) D {
		gtx.Constraints.Min.X = gtx.Constraints.Max.X
		e := material.Editor(th, ed, dica)
		e.TextSize = 14
		e.Color = corTexto
		e.HintColor = corTerciaria
		e.SelectionColor = color.NRGBA{R: 0x00, G: 0x67, B: 0xC0, A: 0x40}
		if !gtx.Enabled() {
			e.Color = corTerciaria
		}
		return e.Layout(gtx)
	})
	call := m.Stop()

	r := image.Rectangle{Max: d.Size}
	raio := gtx.Dp(4)
	borda := corBordaCampo
	if invalido {
		borda = corErro
	}
	retangulo(gtx, r, raio, borda)
	fundo := corCartao
	if !gtx.Enabled() {
		fundo = rgb(0xF7F7F7)
	}
	retangulo(gtx, r.Inset(gtx.Dp(1)), raio-gtx.Dp(1), fundo)
	switch {
	case foco:
		retangulo(gtx, image.Rect(gtx.Dp(1), r.Max.Y-gtx.Dp(2), r.Max.X-gtx.Dp(1), r.Max.Y), 0, corAcento)
	case !invalido:
		retangulo(gtx, image.Rect(raio, r.Max.Y-gtx.Dp(1), r.Max.X-raio, r.Max.Y), 0, rgb(0x868686))
	}
	call.Add(gtx.Ops)
	return d
}

func barraProgresso(gtx C, fracao float32, cor color.NRGBA) D {
	h := gtx.Dp(6)
	w := gtx.Constraints.Max.X
	retangulo(gtx, image.Rect(0, 0, w, h), h/2, corTrilho)
	if f := min(max(fracao, 0), 1); f > 0 {
		retangulo(gtx, image.Rect(0, 0, max(h, int(float32(w)*f)), h), h/2, cor)
	}
	return D{Size: image.Pt(w, h)}
}

// selo é uma etiqueta colorida pequena ("acima do limite").
func selo(gtx C, th *material.Theme, s string, cor, fundo color.NRGBA) D {
	m := op.Record(gtx.Ops)
	d := layout.Inset{Left: 6, Right: 6, Top: 1, Bottom: 1}.Layout(gtx, texto(th, 11, cor, font.Medium, s).Layout)
	call := m.Stop()
	retangulo(gtx, image.Rectangle{Max: d.Size}, gtx.Dp(4), fundo)
	call.Add(gtx.Ops)
	return d
}

func preencherFundo(gtx C, cor color.NRGBA) {
	paint.FillShape(gtx.Ops, cor, clip.Rect{Max: gtx.Constraints.Max}.Op())
}
