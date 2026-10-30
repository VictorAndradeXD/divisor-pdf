package divisor

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"slices"
)

// Atributos que uma página herda dos nós acima dela na árvore.
var herdaveis = []nome{"Resources", "MediaBox", "CropBox", "Rotate"}

type paginaD struct {
	r    ref
	d    dicionario
	herd dicionario
}

// motorDireto copia os objetos de cada parte direto do arquivo original, com
// os mesmos números, sem carregar o conteúdo (imagens, textos) na memória.
// Só as páginas, a árvore de páginas e o catálogo são reescritos.
type motorDireto struct {
	a    *arquivoPDF
	pags []paginaD

	// Estado da parte em planejamento: objetos já incluídos e, para cada
	// página, os que ela acrescentou (para poder descartar a última).
	vistos map[int]*objLido
	porPag [][]int
	extras []int // objetos do catálogo e do trailer
}

func novoMotorDireto(a *arquivoPDF, aoListar func(feitas, total int)) (*motorDireto, error) {
	m := &motorDireto{a: a, vistos: map[int]*objLido{}}
	if err := m.listarPaginas(aoListar); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *motorDireto) totalPaginas() int { return len(m.pags) }

func (m *motorDireto) catalogo() (dicionario, error) {
	r, ok := m.a.trailer["Root"].(ref)
	if !ok {
		return nil, errors.New("catálogo ausente")
	}
	o, err := m.a.ler(r.num)
	if err != nil || o == nil {
		return nil, fmt.Errorf("catálogo ilegível: %w", err)
	}
	d, ok := o.v.(dicionario)
	if !ok {
		return nil, errors.New("catálogo inválido")
	}
	return d, nil
}

func (m *motorDireto) listarPaginas(aoListar func(feitas, total int)) error {
	cat, err := m.catalogo()
	if err != nil {
		return err
	}
	raiz, ok := cat["Pages"].(ref)
	if !ok {
		return errors.New("árvore de páginas ausente")
	}
	total := 0
	if o, _ := m.a.ler(raiz.num); o != nil {
		if d, ok := o.v.(dicionario); ok {
			if n, ok := d["Count"].(numero); ok {
				v, _ := n.inteiro()
				total = int(v)
			}
		}
	}

	type item struct {
		r    ref
		herd dicionario
	}
	visitados := map[int]bool{}
	pilha := []item{{raiz, dicionario{}}}
	for len(pilha) > 0 {
		it := pilha[len(pilha)-1]
		pilha = pilha[:len(pilha)-1]
		if visitados[it.r.num] {
			continue
		}
		visitados[it.r.num] = true
		o, err := m.a.ler(it.r.num)
		if err != nil {
			return err
		}
		if o == nil {
			continue // nó ausente: os leitores de PDF ignoram
		}
		d, ok := o.v.(dicionario)
		if !ok {
			continue
		}
		filhos, temFilhos := m.resolver(d["Kids"]).(array)
		if !temFilhos || d.nome("Type") == "Page" {
			m.pags = append(m.pags, paginaD{r: ref{o.num, o.ger}, d: d, herd: it.herd})
			if aoListar != nil && len(m.pags)%500 == 0 {
				aoListar(len(m.pags), total)
			}
			continue
		}
		herd := it.herd
		for _, k := range herdaveis {
			if v, ok := d[k]; ok {
				herd = clonarRaso(herd)
				herd[k] = v
			}
		}
		for i := len(filhos) - 1; i >= 0; i-- {
			if r, ok := filhos[i].(ref); ok {
				pilha = append(pilha, item{r, herd})
			}
		}
	}
	if len(m.pags) == 0 {
		return errors.New("o PDF não tem páginas")
	}
	return nil
}

func clonarRaso(d dicionario) dicionario {
	n := make(dicionario, len(d)+1)
	for k, v := range d {
		n[k] = v
	}
	return n
}

// resolver segue uma referência direta para o valor.
func (m *motorDireto) resolver(v objeto) objeto {
	if r, ok := v.(ref); ok {
		if o, err := m.a.ler(r.num); err == nil && o != nil {
			return o.v
		}
		return nil
	}
	return v
}

func (m *motorDireto) novoArquivo() error {
	clear(m.vistos)
	m.porPag = m.porPag[:0]
	m.extras = m.extras[:0]

	// Itens do trailer e do catálogo que vão em todas as partes.
	var raizes []objeto
	if v := m.a.trailer["Info"]; v != nil {
		raizes = append(raizes, v)
	}
	if v := m.a.trailer["Encrypt"]; v != nil {
		raizes = append(raizes, v)
	}
	cat, err := m.catalogo()
	if err != nil {
		return err
	}
	if v := cat["OCProperties"]; v != nil {
		raizes = append(raizes, v)
	}
	_, err = m.percorrer(raizes, &m.extras)
	return err
}

// custo inclui a página i (1 = primeira) na parte atual e devolve quantos
// bytes ela acrescenta.
func (m *motorDireto) custo(i int) (int64, error) {
	p := m.pags[i-1]
	var novos []int
	raizes := make([]objeto, 0, len(p.d)+len(p.herd))
	for k, v := range p.d {
		if k != "Parent" {
			raizes = append(raizes, v)
		}
	}
	for k, v := range p.herd {
		if _, proprio := p.d[k]; !proprio {
			raizes = append(raizes, v)
		}
	}
	total, err := m.percorrer(raizes, &novos)
	m.porPag = append(m.porPag, novos)
	return total + 200 + int64(len(p.d))*24, err
}

// percorrer inclui na parte os objetos alcançáveis a partir de raizes.
// Não entra em outras páginas nem sobe por /Parent: um link para a página
// 30.000 não pode arrastar o documento inteiro para a parte.
func (m *motorDireto) percorrer(raizes []objeto, novos *[]int) (int64, error) {
	var total int64
	pilha := raizes
	for len(pilha) > 0 {
		v := pilha[len(pilha)-1]
		pilha = pilha[:len(pilha)-1]
		switch v := v.(type) {
		case ref:
			if _, ok := m.vistos[v.num]; ok {
				continue
			}
			o, err := m.a.ler(v.num)
			if err != nil {
				return total, err
			}
			m.vistos[v.num] = nil
			if o == nil {
				continue
			}
			if d, ok := o.v.(dicionario); ok {
				if t := d.nome("Type"); t == "Page" || t == "Pages" {
					continue
				}
			}
			if o.stm != 0 && m.a.trailer["Encrypt"] != nil {
				return total, errUsarPdfcpu
			}
			if o.stm == 0 {
				o.bruto = m.a.s.trecho(o.ini, o.fim) // ainda em cache: evita reler na gravação
			}
			m.vistos[v.num] = o
			*novos = append(*novos, v.num)
			total += o.fim - o.ini + 40
			if o.stream {
				total += o.dadosLen + 30
			}
			pilha = append(pilha, o.v)
			if !o.lenCorrigido {
				o.v = nil // só o dicionário corrigido precisa ser regravado
			}
		case dicionario:
			for k, x := range v {
				if k != "Parent" {
					pilha = append(pilha, x)
				}
			}
		case array:
			pilha = append(pilha, v...)
		}
	}
	return total, nil
}

// gravar escreve a parte com as páginas de..ate, usando os objetos
// incluídos pelo planejamento (as chamadas a custo de de até ate).
func (m *motorDireto) gravar(c context.Context, de, ate int, w io.Writer, _ func(float64)) error {
	e := &escritor{w: bufio.NewWriterSize(w, 1<<20), a: m.a, c: c, empacotar: m.a.trailer["Encrypt"] == nil}
	e.proximo = len(m.a.xref)

	cat, err := m.catalogo()
	if err != nil {
		return err
	}
	versao := "1.7"
	if m.a.versao == "2.0" {
		versao = "2.0"
	}
	e.escreverStr("%PDF-" + versao + "\n%\xe2\xe3\xcf\xd3\n")

	// Objetos copiados, na ordem em que estão no arquivo (leitura sequencial).
	var objs []*objLido
	for _, n := range m.extras {
		objs = append(objs, m.vistos[n])
	}
	for _, lista := range m.porPag[:ate-de+1] {
		for _, n := range lista {
			objs = append(objs, m.vistos[n])
		}
	}
	slices.SortFunc(objs, func(x, y *objLido) int {
		if x.stm != y.stm {
			return x.stm - y.stm
		}
		return int(x.ini - y.ini)
	})
	for _, o := range objs {
		if err := e.copiar(o); err != nil {
			return err
		}
	}

	// Árvore de páginas nova, equilibrada, e as páginas apontando para ela.
	pags := m.pags[de-1 : ate]
	raiz := e.novoNum()
	if err := e.arvore(pags, raiz, nil); err != nil {
		return err
	}

	novoCat := dicionario{"Type": nome("Catalog"), "Pages": ref{raiz, 0}}
	if v, ok := cat["OCProperties"]; ok {
		novoCat["OCProperties"] = v
	}
	if v, ok := cat["Lang"]; ok && m.a.trailer["Encrypt"] == nil {
		novoCat["Lang"] = v
	}
	numCat := e.novoNum()
	if err := e.objetoNovo(numCat, 0, novoCat); err != nil {
		return err
	}
	return e.finalizar(numCat, m.a.trailer)
}

const ramosSaida = 64

// arvore grava os nós da árvore de páginas sob num.
func (e *escritor) arvore(pags []paginaD, num int, pai *ref) error {
	no := dicionario{"Type": nome("Pages"), "Count": numero(fmt.Sprint(len(pags)))}
	if pai != nil {
		no["Parent"] = *pai
	}
	var filhos array
	eu := ref{num, 0}
	if len(pags) <= ramosSaida {
		for _, p := range pags {
			d := clonarRaso(p.d)
			d["Parent"] = eu
			for k, v := range p.herd {
				if _, ok := d[k]; !ok {
					d[k] = v
				}
			}
			if err := e.objetoNovo(p.r.num, p.r.ger, d); err != nil {
				return err
			}
			filhos = append(filhos, p.r)
		}
	} else {
		tam := ramosSaida
		for tam*ramosSaida < len(pags) {
			tam *= ramosSaida
		}
		for i := 0; i < len(pags); i += tam {
			n := e.novoNum()
			if err := e.arvore(pags[i:min(i+tam, len(pags))], n, &eu); err != nil {
				return err
			}
			filhos = append(filhos, ref{n, 0})
		}
	}
	no["Kids"] = filhos
	return e.objetoNovo(num, 0, no)
}

// escritor monta o PDF de saída.
type escritor struct {
	w         *bufio.Writer
	a         *arquivoPDF
	c         context.Context
	pos       int64
	proximo   int
	empacotar bool // objetos simples vão para object streams comprimidos

	xref   map[int]entrada
	pacote bytes.Buffer
	indice []int // pares num, deslocamento do pacote em montagem
}

func (e *escritor) novoNum() int { e.proximo++; return e.proximo - 1 }

func (e *escritor) escreverStr(s string) { n, _ := e.w.WriteString(s); e.pos += int64(n) }
func (e *escritor) escreverB(b []byte)   { n, _ := e.w.Write(b); e.pos += int64(n) }

func (e *escritor) registrar(num int, en entrada) {
	if e.xref == nil {
		e.xref = map[int]entrada{}
	}
	e.xref[num] = en
}

// copiar grava um objeto do arquivo original.
func (e *escritor) copiar(o *objLido) error {
	if err := e.c.Err(); err != nil {
		return err
	}
	var valor []byte
	if o.stm != 0 {
		m, err := e.a.objStm(o.stm)
		if err != nil {
			return err
		}
		valor = m[o.ini:o.fim]
	} else if !o.stream || !o.lenCorrigido {
		valor = o.bruto
	}

	if !o.stream {
		if e.empacotar && o.ger == 0 {
			return e.empacotarObj(o.num, valor)
		}
		e.registrar(o.num, entrada{tipo: 1, off: e.pos, ger: uint16(o.ger)})
		e.escreverStr(fmt.Sprintf("%d %d obj\n", o.num, o.ger))
		e.escreverB(valor)
		e.escreverStr("\nendobj\n")
		return nil
	}

	if o.lenCorrigido {
		d := clonarRaso(o.v.(dicionario))
		d["Length"] = numero(fmt.Sprint(o.dadosLen))
		var b bytes.Buffer
		escrever(&b, d)
		valor = b.Bytes()
	}
	e.registrar(o.num, entrada{tipo: 1, off: e.pos, ger: uint16(o.ger)})
	e.escreverStr(fmt.Sprintf("%d %d obj\n", o.num, o.ger))
	e.escreverB(valor)
	e.escreverStr("\nstream\n")
	n, err := io.Copy(e.w, io.NewSectionReader(e.a.s.f, o.dadosIni, o.dadosLen))
	e.pos += n
	if err != nil {
		return err
	}
	e.escreverStr("\nendstream\nendobj\n")
	return nil
}

// objetoNovo grava um objeto montado pelo programa.
func (e *escritor) objetoNovo(num, ger int, v objeto) error {
	var b bytes.Buffer
	escrever(&b, v)
	if e.empacotar && ger == 0 {
		return e.empacotarObj(num, b.Bytes())
	}
	e.registrar(num, entrada{tipo: 1, off: e.pos, ger: uint16(ger)})
	e.escreverStr(fmt.Sprintf("%d %d obj\n", num, ger))
	e.escreverB(b.Bytes())
	e.escreverStr("\nendobj\n")
	return e.c.Err()
}

const objsPorPacote = 200

func (e *escritor) empacotarObj(num int, valor []byte) error {
	e.indice = append(e.indice, num, e.pacote.Len())
	e.pacote.Write(valor)
	e.pacote.WriteByte('\n')
	if len(e.indice)/2 >= objsPorPacote {
		return e.fecharPacote()
	}
	return nil
}

// fecharPacote grava os objetos acumulados num object stream comprimido.
func (e *escritor) fecharPacote() error {
	if len(e.indice) == 0 {
		return nil
	}
	num := e.novoNum()
	var cab bytes.Buffer
	for i := 0; i < len(e.indice); i += 2 {
		fmt.Fprintf(&cab, "%d %d ", e.indice[i], e.indice[i+1])
		e.registrar(e.indice[i], entrada{tipo: 2, off: int64(num), idx: int32(i / 2)})
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(cab.Bytes())
	zw.Write(e.pacote.Bytes())
	zw.Close()

	e.registrar(num, entrada{tipo: 1, off: e.pos})
	e.escreverStr(fmt.Sprintf("%d 0 obj\n<</Type/ObjStm/N %d/First %d/Filter/FlateDecode/Length %d>>\nstream\n",
		num, len(e.indice)/2, cab.Len(), z.Len()))
	e.escreverB(z.Bytes())
	e.escreverStr("\nendstream\nendobj\n")
	e.pacote.Reset()
	e.indice = e.indice[:0]
	return e.c.Err()
}

// finalizar grava a xref (como xref stream) e o trailer.
func (e *escritor) finalizar(raiz int, trailerOrig dicionario) error {
	if err := e.fecharPacote(); err != nil {
		return err
	}
	num := e.novoNum()
	e.registrar(num, entrada{tipo: 1, off: e.pos})

	nums := make([]int, 0, len(e.xref))
	for n := range e.xref {
		nums = append(nums, n)
	}
	slices.Sort(nums)
	var indice array
	var dados bytes.Buffer
	for i := 0; i < len(nums); {
		j := i
		for j+1 < len(nums) && nums[j+1] == nums[j]+1 {
			j++
		}
		indice = append(indice, numero(fmt.Sprint(nums[i])), numero(fmt.Sprint(j-i+1)))
		for _, n := range nums[i : j+1] {
			en := e.xref[n]
			var linha [8]byte
			linha[0] = en.tipo
			var off [8]byte
			binary.BigEndian.PutUint64(off[:], uint64(en.off))
			copy(linha[1:6], off[3:])
			c3 := uint16(en.ger)
			if en.tipo == 2 {
				c3 = uint16(en.idx)
			}
			binary.BigEndian.PutUint16(linha[6:], c3)
			dados.Write(linha[:])
		}
		i = j + 1
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write(dados.Bytes())
	zw.Close()

	d := dicionario{
		"Type": nome("XRef"), "Size": numero(fmt.Sprint(num + 1)), "Root": ref{raiz, 0},
		"W": array{numero("1"), numero("5"), numero("2")}, "Index": indice,
		"Filter": nome("FlateDecode"), "Length": numero(fmt.Sprint(z.Len())),
	}
	for _, k := range []nome{"Info", "ID", "Encrypt"} {
		if v, ok := trailerOrig[k]; ok {
			d[k] = v
		}
	}
	inicio := e.pos
	var b bytes.Buffer
	escrever(&b, d)
	e.escreverStr(fmt.Sprintf("%d 0 obj\n", num))
	e.escreverB(b.Bytes())
	e.escreverStr("\nstream\n")
	e.escreverB(z.Bytes())
	e.escreverStr(fmt.Sprintf("\nendstream\nendobj\nstartxref\n%d\n%%%%EOF\n", inicio))
	return e.w.Flush()
}
