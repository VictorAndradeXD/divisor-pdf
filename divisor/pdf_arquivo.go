package divisor

import (
	"bytes"
	"compress/zlib"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// errUsarPdfcpu indica um PDF que o motor de cópia direta não trata
// (criptografia com object streams, filtros incomuns) e que deve ir para o
// motor completo, baseado no pdfcpu.
var errUsarPdfcpu = errors.New("PDF precisa do motor completo")

const (
	blocoFonte = 16 << 10
	cacheFonte = 2048 // 32 MB
)

// fonte lê o arquivo em blocos com cache. Os dicionários de um PDF são
// pequenos e ficam espalhados entre streams grandes; blocos de 64 KB evitam
// ler o conteúdo das imagens só para chegar até eles.
type fonte struct {
	f     *os.File
	tam   int64
	cache map[int64][]byte
	ordem []int64

	ultIni int64 // atalho para o último bloco usado
	ult    []byte
}

func novaFonte(f *os.File) (*fonte, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return &fonte{f: f, tam: st.Size(), cache: map[int64][]byte{}, ultIni: -1}, nil
}

func (s *fonte) byteEm(pos int64) (byte, bool) {
	if pos < 0 || pos >= s.tam {
		return 0, false
	}
	if s.ultIni >= 0 && pos >= s.ultIni && pos < s.ultIni+int64(len(s.ult)) {
		return s.ult[pos-s.ultIni], true
	}
	i := pos / blocoFonte
	b, ok := s.cache[i]
	if !ok {
		if len(s.cache) >= cacheFonte {
			velho := s.ordem[0]
			s.ordem = s.ordem[1:]
			b = s.cache[velho][:blocoFonte]
			delete(s.cache, velho)
		} else {
			b = make([]byte, blocoFonte)
		}
		n, err := s.f.ReadAt(b, i*blocoFonte)
		if n == 0 && err != nil {
			return 0, false
		}
		b = b[:n]
		s.cache[i] = b
		s.ordem = append(s.ordem, i)
	}
	s.ultIni, s.ult = i*blocoFonte, b
	if pos-s.ultIni >= int64(len(b)) {
		return 0, false
	}
	return b[pos-s.ultIni], true
}

// trecho copia os bytes [ini, fim), de preferência dos blocos em cache.
func (s *fonte) trecho(ini, fim int64) []byte {
	b := make([]byte, 0, max(fim-ini, 0))
	for p := ini; p < fim; {
		if _, ok := s.byteEm(p); !ok {
			break
		}
		n := min(fim, s.ultIni+int64(len(s.ult))) - p
		b = append(b, s.ult[p-s.ultIni:p-s.ultIni+n]...)
		p += n
	}
	return b
}

// ler copia n bytes a partir de pos (menos, no fim do arquivo).
func (s *fonte) ler(pos int64, n int64) []byte {
	n = max(min(n, s.tam-pos), 0)
	b := make([]byte, n)
	m, _ := s.f.ReadAt(b, pos)
	return b[:m]
}

type entrada struct {
	tipo uint8 // 0: livre/ausente, 1: no arquivo, 2: dentro de object stream
	ger  uint16
	off  int64 // tipo 1: posição; tipo 2: número do object stream
	idx  int32 // tipo 2: índice dentro do object stream
}

// objLido é um objeto indireto: o valor interpretado e onde estão seus bytes
// originais, para copiá-los sem reinterpretar.
type objLido struct {
	num, ger int
	v        objeto
	ini, fim int64  // bytes do valor (no arquivo ou no object stream)
	stm      int    // object stream de origem; 0 se está solto no arquivo
	bruto    []byte // os bytes do valor, guardados quando o objeto entra numa parte

	stream       bool
	dadosIni     int64
	dadosLen     int64
	lenCorrigido bool // /Length estava errado: o dicionário precisa ser regravado
}

type arquivoPDF struct {
	s        *fonte
	xref     []entrada
	trailer  dicionario
	versao   string
	reparado bool

	stms  map[int]memoria // object streams descomprimidos (cache pequeno)
	ordem []int
}

func abrirPDF(f *os.File) (*arquivoPDF, error) {
	s, err := novaFonte(f)
	if err != nil {
		return nil, err
	}
	a := &arquivoPDF{s: s, stms: map[int]memoria{}}
	cab := s.ler(0, 1024)
	i := bytes.Index(cab, []byte("%PDF-"))
	if i < 0 {
		return nil, ErrNaoEhPDF
	}
	a.versao = string(bytes.TrimRight(cab[i+5:min(i+8, len(cab))], "\r\n %"))

	if err := a.lerXref(); err != nil || a.trailer["Root"] == nil {
		if err := a.reconstruir(); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// lerXref percorre as seções de referência cruzada a partir da última.
func (a *arquivoPDF) lerXref() error {
	fim := a.s.ler(max(a.s.tam-4096, 0), 4096)
	i := bytes.LastIndex(fim, []byte("startxref"))
	if i < 0 {
		return errors.New("startxref não encontrado")
	}
	l := &lexico{j: memoria(fim), pos: int64(i + len("startxref"))}
	l.pularEspacos()
	pos, err := strconv.ParseInt(l.palavraCrua(), 10, 64)
	if err != nil {
		return errors.New("startxref inválido")
	}

	conhecidos := map[int]bool{}
	visitados := map[int64]bool{}
	for pos > 0 && !visitados[pos] {
		visitados[pos] = true
		secao, trailer, err := a.lerSecao(pos)
		if err != nil {
			return err
		}
		if stm, ok := trailer["XRefStm"].(numero); ok { // arquivo híbrido
			if p, ok := stm.inteiro(); ok {
				if extra, _, err := a.lerSecao(p); err == nil {
					for num, e := range extra {
						if s, ok := secao[num]; !ok || s.tipo == 0 {
							secao[num] = e
						}
					}
				}
			}
		}
		for num, e := range secao { // a seção mais nova prevalece
			if !conhecidos[num] {
				conhecidos[num] = true
				a.definir(num, e)
			}
		}
		if a.trailer == nil {
			a.trailer = trailer
		}
		pos = 0
		if p, ok := trailer["Prev"].(numero); ok {
			pos, _ = p.inteiro()
		}
	}
	if a.trailer == nil {
		return errors.New("trailer não encontrado")
	}
	return a.conferirXref()
}

func (a *arquivoPDF) definir(num int, e entrada) {
	if num < 0 || num > 50_000_000 {
		return
	}
	if num >= len(a.xref) {
		a.xref = append(a.xref, make([]entrada, num+1-len(a.xref))...)
	}
	a.xref[num] = e
}

// conferirXref testa algumas entradas: uma xref com posições deslocadas é
// comum em arquivos editados por programas descuidados.
func (a *arquivoPDF) conferirXref() error {
	testados := 0
	for num := len(a.xref) - 1; num > 0 && testados < 20; num -= max(len(a.xref)/20, 1) {
		if e := a.xref[num]; e.tipo == 1 {
			testados++
			if !a.cabecalhoEm(e.off, num) {
				return fmt.Errorf("xref aponta para posição errada (obj %d)", num)
			}
		}
	}
	return nil
}

func (a *arquivoPDF) cabecalhoEm(pos int64, num int) bool {
	l := &lexico{j: a.s, pos: pos}
	l.pularEspacos()
	n, err := strconv.Atoi(l.palavraCrua())
	if err != nil || n != num {
		return false
	}
	l.pularEspacos()
	l.palavraCrua()
	l.pularEspacos()
	return l.palavraCrua() == "obj"
}

func (a *arquivoPDF) lerSecao(pos int64) (map[int]entrada, dicionario, error) {
	l := &lexico{j: a.s, pos: pos}
	l.pularEspacos()
	inicio := l.pos
	if l.palavraCrua() == "xref" {
		return a.lerTabela(l)
	}
	l.pos = inicio
	o, err := a.lerEm(inicio, -1)
	if err != nil {
		return nil, nil, fmt.Errorf("xref inválida: %w", err)
	}
	d, _ := o.v.(dicionario)
	if !o.stream || d.nome("Type") != "XRef" {
		return nil, nil, errors.New("xref inválida")
	}
	dados, err := a.decodificar(o)
	if err != nil {
		return nil, nil, fmt.Errorf("xref stream: %w", err)
	}
	return lerXrefStream(d, dados)
}

func (a *arquivoPDF) lerTabela(l *lexico) (map[int]entrada, dicionario, error) {
	secao := map[int]entrada{}
	for {
		l.pularEspacos()
		p := l.palavraCrua()
		if p == "trailer" {
			v, err := l.valor()
			if err != nil {
				return nil, nil, fmt.Errorf("trailer: %w", err)
			}
			d, ok := v.(dicionario)
			if !ok {
				return nil, nil, errors.New("trailer inválido")
			}
			return secao, d, nil
		}
		ini, err1 := strconv.Atoi(p)
		l.pularEspacos()
		qtd, err2 := strconv.Atoi(l.palavraCrua())
		if err1 != nil || err2 != nil || qtd < 0 {
			return nil, nil, errors.New("tabela xref inválida")
		}
		for i := range qtd {
			l.pularEspacos()
			off, err1 := strconv.ParseInt(l.palavraCrua(), 10, 64)
			l.pularEspacos()
			ger, err2 := strconv.Atoi(l.palavraCrua())
			l.pularEspacos()
			t := l.palavraCrua()
			if err1 != nil || err2 != nil || (t != "n" && t != "f") {
				return nil, nil, errors.New("entrada xref inválida")
			}
			e := entrada{ger: uint16(ger)}
			if t == "n" && off > 0 {
				e.tipo, e.off = 1, off
			}
			secao[ini+i] = e
		}
	}
}

func lerXrefStream(d dicionario, dados []byte) (map[int]entrada, dicionario, error) {
	var w [3]int
	ws, _ := d["W"].(array)
	if len(ws) != 3 {
		return nil, nil, errors.New("xref stream sem /W")
	}
	for i, v := range ws {
		n := inteiro(v)
		w[i] = int(n)
	}
	larg := w[0] + w[1] + w[2]
	if larg <= 0 {
		return nil, nil, errors.New("xref stream com /W inválido")
	}
	var faixas []int
	if idx, ok := d["Index"].(array); ok {
		for _, v := range idx {
			n := inteiro(v)
			faixas = append(faixas, int(n))
		}
	} else {
		n := inteiro(d["Size"])
		faixas = []int{0, int(n)}
	}

	campo := func(b []byte) int64 {
		var v int64
		for _, c := range b {
			v = v<<8 | int64(c)
		}
		return v
	}
	secao := map[int]entrada{}
	p := 0
	for f := 0; f+1 < len(faixas); f += 2 {
		for num := faixas[f]; num < faixas[f]+faixas[f+1]; num++ {
			if p+larg > len(dados) {
				return secao, d, nil
			}
			linha := dados[p : p+larg]
			p += larg
			tipo := int64(1)
			if w[0] > 0 {
				tipo = campo(linha[:w[0]])
			}
			c2, c3 := campo(linha[w[0]:w[0]+w[1]]), campo(linha[w[0]+w[1]:])
			switch tipo {
			case 1:
				secao[num] = entrada{tipo: 1, off: c2, ger: uint16(c3)}
			case 2:
				secao[num] = entrada{tipo: 2, off: c2, idx: int32(c3)}
			default:
				secao[num] = entrada{}
			}
		}
	}
	return secao, d, nil
}

// ler devolve o objeto indireto num, ou nil se ele não existe.
func (a *arquivoPDF) ler(num int) (*objLido, error) {
	if num <= 0 || num >= len(a.xref) {
		return nil, nil
	}
	e := a.xref[num]
	switch e.tipo {
	case 1:
		o, err := a.lerEm(e.off, num)
		if err != nil && !a.reparado {
			if errRec := a.reconstruir(); errRec != nil {
				return nil, err
			}
			return a.ler(num)
		}
		return o, err
	case 2:
		return a.lerDeStm(num, int(e.off), int(e.idx))
	}
	return nil, nil
}

// lerEm lê "num ger obj ... endobj" na posição pos. num < 0 aceita qualquer número.
func (a *arquivoPDF) lerEm(pos int64, num int) (*objLido, error) {
	l := &lexico{j: a.s, pos: pos}
	l.pularEspacos()
	n, err1 := strconv.Atoi(l.palavraCrua())
	l.pularEspacos()
	g, err2 := strconv.Atoi(l.palavraCrua())
	l.pularEspacos()
	if err1 != nil || err2 != nil || l.palavraCrua() != "obj" || (num >= 0 && n != num) {
		return nil, fmt.Errorf("objeto %d não está na posição %d", num, pos)
	}
	l.pularEspacos()
	o := &objLido{num: n, ger: g, ini: l.pos}
	if o.v, err1 = l.valor(); err1 != nil {
		return nil, fmt.Errorf("objeto %d: %w", n, err1)
	}
	o.fim = l.pos

	d, ehDict := o.v.(dicionario)
	l.pularEspacos()
	volta := l.pos
	if !ehDict || l.palavraCrua() != "stream" {
		l.pos = volta
		return o, nil
	}
	if b, _ := l.espiar(); b == '\r' {
		l.pos++
	}
	if b, _ := l.espiar(); b == '\n' {
		l.pos++
	}
	o.stream, o.dadosIni = true, l.pos
	o.dadosLen = -1
	switch v := d["Length"].(type) {
	case numero:
		o.dadosLen, _ = v.inteiro()
	case ref:
		if r, err := a.ler(v.num); err == nil && r != nil {
			if n, ok := r.v.(numero); ok {
				o.dadosLen, _ = n.inteiro()
			}
		}
	}
	if o.dadosLen < 0 || !a.terminaEm(o.dadosIni+o.dadosLen) {
		o.dadosLen = a.procurarFim(o.dadosIni)
		o.lenCorrigido = true
	}
	return o, nil
}

func (a *arquivoPDF) terminaEm(pos int64) bool {
	if pos > a.s.tam {
		return false
	}
	l := &lexico{j: a.s, pos: pos}
	l.pularEspacos()
	return l.palavraCrua() == "endstream"
}

// procurarFim acha "endstream" a partir de ini e devolve o tamanho real dos dados.
func (a *arquivoPDF) procurarFim(ini int64) int64 {
	alvo := []byte("endstream")
	for pos := ini; pos < a.s.tam; pos += 1 << 20 {
		trecho := a.s.ler(pos, 1<<20+int64(len(alvo)))
		if i := bytes.Index(trecho, alvo); i >= 0 {
			fim := pos + int64(i)
			for fim > ini {
				b, _ := a.s.byteEm(fim - 1)
				if b != '\n' && b != '\r' {
					break
				}
				fim--
			}
			return fim - ini
		}
	}
	return a.s.tam - ini
}

func (a *arquivoPDF) lerDeStm(num, stm, idx int) (*objLido, error) {
	dados, err := a.objStm(stm)
	if err != nil {
		return nil, err
	}
	pos, err := posicaoNoStm(dados, num, idx)
	if err != nil {
		return nil, err
	}
	l := &lexico{j: dados, pos: pos}
	l.pularEspacos()
	o := &objLido{num: num, ini: l.pos, stm: stm}
	if o.v, err = l.valor(); err != nil {
		return nil, fmt.Errorf("objeto %d (no object stream %d): %w", num, stm, err)
	}
	o.fim = l.pos
	return o, nil
}

// posicaoNoStm acha o objeto num no índice "num1 pos1 num2 pos2 ..." do object stream.
func posicaoNoStm(dados memoria, num, idx int) (int64, error) {
	cab := dados.cabecalhoStm
	first, pares := cab()
	if idx >= 0 && idx < len(pares) && pares[idx][0] == int64(num) {
		return first + pares[idx][1], nil
	}
	for _, p := range pares { // índice da xref errado: procura pelo número
		if p[0] == int64(num) {
			return first + p[1], nil
		}
	}
	return 0, fmt.Errorf("objeto %d ausente do object stream", num)
}

// Os primeiros bytes do object stream descomprimido guardam /First e /N;
// eles são copiados para o início do buffer em objStm.
func (m memoria) cabecalhoStm() (int64, [][2]int64) {
	l := &lexico{j: m}
	l.pularEspacos()
	first, _ := strconv.ParseInt(l.palavraCrua(), 10, 64)
	l.pularEspacos()
	n, _ := strconv.Atoi(l.palavraCrua())
	pares := make([][2]int64, 0, n)
	for range n {
		l.pularEspacos()
		a, err1 := strconv.ParseInt(l.palavraCrua(), 10, 64)
		l.pularEspacos()
		b, err2 := strconv.ParseInt(l.palavraCrua(), 10, 64)
		if err1 != nil || err2 != nil {
			break
		}
		pares = append(pares, [2]int64{a, b})
	}
	return first, pares
}

// objStm devolve o object stream num descomprimido, prefixado por
// "<First+prefixo> <N> " para que as posições internas continuem válidas.
func (a *arquivoPDF) objStm(num int) (memoria, error) {
	if m, ok := a.stms[num]; ok {
		return m, nil
	}
	if a.trailer["Encrypt"] != nil {
		return nil, errUsarPdfcpu
	}
	o, err := a.ler(num)
	if err != nil || o == nil || !o.stream {
		return nil, fmt.Errorf("object stream %d inválido", num)
	}
	d := o.v.(dicionario)
	dados, err := a.decodificar(o)
	if err != nil {
		return nil, fmt.Errorf("object stream %d: %w", num, err)
	}
	first := inteiro(d["First"])
	n := inteiro(d["N"])
	prefixo := []byte(fmt.Sprintf("%020d %020d ", 0, n))
	copy(prefixo, fmt.Sprintf("%020d", first+int64(len(prefixo))))
	m := memoria(append(prefixo, dados...))

	if len(a.stms) >= 16 {
		delete(a.stms, a.ordem[0])
		a.ordem = a.ordem[1:]
	}
	a.stms[num] = m
	a.ordem = append(a.ordem, num)
	return m, nil
}

// decodificar lê e descomprime um stream pequeno (xref ou object stream).
func (a *arquivoPDF) decodificar(o *objLido) ([]byte, error) {
	d := o.v.(dicionario)
	if o.dadosLen > 256<<20 {
		return nil, errors.New("stream grande demais")
	}
	dados := a.s.ler(o.dadosIni, o.dadosLen)
	var filtro nome
	var parms dicionario
	switch f := d["Filter"].(type) {
	case nil:
		return dados, nil
	case nome:
		filtro = f
		parms, _ = d["DecodeParms"].(dicionario)
	case array:
		if len(f) != 1 {
			return nil, errUsarPdfcpu
		}
		filtro, _ = f[0].(nome)
		if ps, ok := d["DecodeParms"].(array); ok && len(ps) == 1 {
			parms, _ = ps[0].(dicionario)
		}
	}
	if filtro != "FlateDecode" {
		return nil, errUsarPdfcpu
	}
	z, err := zlib.NewReader(bytes.NewReader(dados))
	if err != nil {
		return nil, err
	}
	saida, err := io.ReadAll(z)
	if err != nil && len(saida) == 0 {
		return nil, err
	}
	return desfazerPreditor(saida, parms)
}

// desfazerPreditor reverte os preditores PNG (usados quase sempre em xref streams).
func desfazerPreditor(b []byte, parms dicionario) ([]byte, error) {
	pred := inteiro(parms["Predictor"])
	if pred < 10 {
		if pred > 1 {
			return nil, errUsarPdfcpu // preditor TIFF: raro
		}
		return b, nil
	}
	cols := int64(1)
	if c, ok := parms["Columns"].(numero); ok {
		cols, _ = c.inteiro()
	}
	cores, bpc := int64(1), int64(8)
	if c, ok := parms["Colors"].(numero); ok {
		cores, _ = c.inteiro()
	}
	if c, ok := parms["BitsPerComponent"].(numero); ok {
		bpc, _ = c.inteiro()
	}
	bpp := int(max((cores*bpc+7)/8, 1))
	linha := int((cols*cores*bpc + 7) / 8)
	if linha <= 0 {
		return nil, errors.New("preditor inválido")
	}
	var saida []byte
	ant := make([]byte, linha)
	for p := 0; p+1+linha <= len(b); p += 1 + linha {
		tipo, atual := b[p], append([]byte(nil), b[p+1:p+1+linha]...)
		for i := range atual {
			var esq, cima, diag byte
			if i >= bpp {
				esq, diag = atual[i-bpp], ant[i-bpp]
			}
			cima = ant[i]
			switch tipo {
			case 1:
				atual[i] += esq
			case 2:
				atual[i] += cima
			case 3:
				atual[i] += byte((int(esq) + int(cima)) / 2)
			case 4:
				atual[i] += paeth(esq, cima, diag)
			}
		}
		saida = append(saida, atual...)
		ant = atual
	}
	return saida, nil
}

func paeth(a, b, c byte) byte {
	p := int(a) + int(b) - int(c)
	pa, pb, pc := abs(p-int(a)), abs(p-int(b)), abs(p-int(c))
	if pa <= pb && pa <= pc {
		return a
	}
	if pb <= pc {
		return b
	}
	return c
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// reconstruir refaz a xref varrendo o arquivo atrás de "num ger obj", como
// fazem os leitores de PDF diante de um arquivo danificado.
func (a *arquivoPDF) reconstruir() error {
	if a.reparado {
		return errors.New("PDF danificado")
	}
	a.reparado = true
	a.xref = nil
	a.stms = map[int]memoria{}
	a.ordem = nil
	var trailer dicionario
	var catalogo, stms []int

	const trecho = 4 << 20
	ultimoObj := -1
	for pos := int64(0); pos < a.s.tam; pos += trecho {
		b := a.s.ler(pos, trecho+64)
		for i := 0; i < len(b) && i < trecho; {
			j := indiceQualquer(b[i:], "obj", "trailer", "/Catalog", "/ObjStm")
			if j < 0 {
				break
			}
			k := i + j
			switch {
			case bytes.HasPrefix(b[k:], []byte("obj")):
				if num, ger, ini, ok := numeroAntes(b, k); ok && (k+3 >= len(b) || !ehRegular(b[k+3])) {
					a.definir(num, entrada{tipo: 1, off: pos + int64(ini), ger: uint16(ger)})
					ultimoObj = num
				}
				k += 3
			case bytes.HasPrefix(b[k:], []byte("trailer")):
				l := &lexico{j: a.s, pos: pos + int64(k) + 7}
				if v, err := l.valor(); err == nil {
					if d, ok := v.(dicionario); ok && d["Root"] != nil {
						trailer = d
					}
				}
				k += 7
			case bytes.HasPrefix(b[k:], []byte("/Catalog")):
				catalogo = append(catalogo, ultimoObj)
				k += 8
			default:
				stms = append(stms, ultimoObj)
				k += 7
			}
			i = k
		}
	}

	for _, num := range stms { // objetos dentro de object streams
		o, err := a.ler(num)
		if err != nil || o == nil || !o.stream {
			continue
		}
		if d, _ := o.v.(dicionario); d.nome("Type") != "ObjStm" {
			continue
		}
		m, err := a.objStm(num)
		if err != nil {
			continue
		}
		_, pares := m.cabecalhoStm()
		for i, p := range pares {
			if n := int(p[0]); n >= len(a.xref) || a.xref[n].tipo == 0 {
				a.definir(n, entrada{tipo: 2, off: int64(num), idx: int32(i)})
			}
		}
	}

	if trailer == nil {
		trailer = dicionario{}
	}
	if r, ok := trailer["Root"].(ref); !ok || !a.ehCatalogo(r.num) {
		delete(trailer, "Root")
		for i := len(catalogo) - 1; i >= 0; i-- {
			if a.ehCatalogo(catalogo[i]) {
				trailer["Root"] = ref{num: catalogo[i]}
				break
			}
		}
	}
	if trailer["Root"] == nil {
		return errors.New("PDF danificado: catálogo não encontrado")
	}
	delete(trailer, "Prev")
	delete(trailer, "XRefStm")
	a.trailer = trailer
	return nil
}

func (a *arquivoPDF) ehCatalogo(num int) bool {
	o, err := a.ler(num)
	if err != nil || o == nil {
		return false
	}
	d, _ := o.v.(dicionario)
	return d.nome("Type") == "Catalog" && d["Pages"] != nil
}

func indiceQualquer(b []byte, alvos ...string) int {
	menor := -1
	for _, t := range alvos {
		if i := bytes.Index(b, []byte(t)); i >= 0 && (menor < 0 || i < menor) {
			menor = i
		}
	}
	return menor
}

func ehRegular(c byte) bool { return !ehEspaco(c) && !ehDelimitador(c) }

// numeroAntes reconhece "num ger " imediatamente antes de b[k:] ("obj").
func numeroAntes(b []byte, k int) (num, ger, ini int, ok bool) {
	i := k
	pular := func() {
		for i > 0 && ehEspaco(b[i-1]) {
			i--
		}
	}
	digitos := func() (int, bool) {
		fim := i
		for i > 0 && b[i-1] >= '0' && b[i-1] <= '9' && fim-i < 10 {
			i--
		}
		if i == fim {
			return 0, false
		}
		v, err := strconv.Atoi(string(b[i:fim]))
		return v, err == nil
	}
	if i == 0 || !ehEspaco(b[i-1]) {
		return
	}
	pular()
	if ger, ok = digitos(); !ok || i == 0 || !ehEspaco(b[i-1]) {
		return 0, 0, 0, false
	}
	pular()
	if num, ok = digitos(); !ok {
		return 0, 0, 0, false
	}
	if i > 0 && ehRegular(b[i-1]) {
		return 0, 0, 0, false
	}
	return num, ger, i, true
}
