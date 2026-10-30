package divisor

import (
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Custo fixo aproximado de um arquivo de saída e de cada página nele
// (catálogo, árvore de páginas, tabela xref).
const (
	custoArquivo = 4 << 10
	custoPagina  = 64
)

// medidor estima quantos bytes cada página acrescenta ao arquivo de saída em
// que ela entra: a própria página mais tudo o que ela referencia (conteúdo,
// imagens, fontes...). Recursos compartilhados, como uma fonte usada em todas
// as páginas, contam uma vez por arquivo.
type medidor struct {
	xt     *model.XRefTable
	tam    map[int]int64 // tamanho de cada objeto, sem as referências
	vistos map[int]bool  // objetos já contados no arquivo atual
	pilha  []types.Object
}

func novoMedidor(xt *model.XRefTable) *medidor {
	return &medidor{xt: xt, tam: map[int]int64{}, vistos: map[int]bool{}}
}

// novoArquivo zera a contagem de recursos compartilhados.
func (m *medidor) novoArquivo() { clear(m.vistos) }

// pagina devolve os bytes que a página acrescenta ao arquivo atual e marca
// seus objetos como já contados.
func (m *medidor) pagina(d types.Dict, ref types.IndirectRef) int64 {
	total := int64(custoPagina)
	if n := ref.ObjectNumber.Value(); !m.vistos[n] {
		m.vistos[n] = true
		total += m.tamanho(n, d)
	}
	for k, v := range d {
		if k != "Parent" {
			m.pilha = append(m.pilha, v)
		}
	}
	if _, ok := d.Find("Resources"); !ok { // herdados de um nó acima
		if r := m.herdado(d, "Resources"); r != nil {
			m.pilha = append(m.pilha, r)
		}
	}

	for len(m.pilha) > 0 {
		o := m.pilha[len(m.pilha)-1]
		m.pilha = m.pilha[:len(m.pilha)-1]

		switch o := o.(type) {
		case types.IndirectRef:
			n := o.ObjectNumber.Value()
			if m.vistos[n] {
				continue
			}
			m.vistos[n] = true
			alvo, err := m.xt.Dereference(o)
			if err != nil || alvo == nil || ehPagina(alvo) {
				continue // links para outras páginas não arrastam essas páginas
			}
			total += m.tamanho(n, alvo)
			m.pilha = append(m.pilha, alvo)
		case types.Dict:
			m.empilharDict(o)
		case types.StreamDict:
			m.empilharDict(o.Dict)
		case types.Array:
			m.pilha = append(m.pilha, o...)
		}
	}
	return total
}

func (m *medidor) empilharDict(d types.Dict) {
	for k, v := range d {
		if k != "Parent" && k != "P" {
			m.pilha = append(m.pilha, v)
		}
	}
}

func (m *medidor) herdado(d types.Dict, chave string) types.Object {
	for range 64 {
		pai, err := m.xt.DereferenceDict(d["Parent"])
		if err != nil || pai == nil {
			return nil
		}
		if v, ok := pai.Find(chave); ok {
			return v
		}
		d = pai
	}
	return nil
}

// tamanho estima os bytes que o objeto ocupa sozinho no arquivo gravado.
func (m *medidor) tamanho(n int, o types.Object) int64 {
	if t, ok := m.tam[n]; ok {
		return t
	}
	var t int64
	switch o := o.(type) {
	case types.StreamDict:
		t = int64(len(o.Raw)+len(o.Dict.PDFString())) + 32
	default:
		t = int64(len(o.PDFString())) + 16
	}
	m.tam[n] = t
	return t
}

func ehPagina(o types.Object) bool {
	d, ok := o.(types.Dict)
	if !ok {
		return false
	}
	t := d.Type()
	return t != nil && (*t == "Page" || *t == "Pages")
}
