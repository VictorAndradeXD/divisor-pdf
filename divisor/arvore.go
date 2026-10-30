package divisor

import (
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Máximo de filhos por nó da árvore de páginas após o balanceamento.
const ramos = 64

// balancear reorganiza, só em memória, nós da árvore de páginas com muitos
// filhos em subnós de até `ramos` filhos. O pdfcpu localiza cada página
// percorrendo a árvore desde a raiz e só consegue pular subárvores inteiras;
// com todas as páginas numa lista só (como o qpdf e muitos programas gravam),
// cada busca é linear e dividir 34 mil páginas fica quadrático.
// Os subnós não têm atributos próprios, então a herança de Resources,
// MediaBox etc. continua igual.
func balancear(xt *model.XRefTable) error {
	raiz, err := xt.Pages()
	if err != nil || raiz == nil {
		return fmt.Errorf("árvore de páginas ausente: %w", err)
	}
	return balancearNo(xt, *raiz, map[int]bool{})
}

func balancearNo(xt *model.XRefTable, ref types.IndirectRef, visitados map[int]bool) error {
	if visitados[ref.ObjectNumber.Value()] {
		return nil
	}
	visitados[ref.ObjectNumber.Value()] = true

	d, err := xt.DereferenceDict(ref)
	if err != nil {
		return err
	}
	filhos, err := xt.DereferenceArray(d["Kids"])
	if err != nil || filhos == nil {
		return err // folha (página) ou sem filhos
	}

	contagens := make([]int, len(filhos))
	for i, o := range filhos {
		kref, ok := o.(types.IndirectRef)
		if !ok {
			return fmt.Errorf("árvore de páginas corrompida")
		}
		if contagens[i], err = contar(xt, kref); err != nil {
			return err
		}
		if contagens[i] > 1 || ehNoPages(xt, kref) {
			if err := balancearNo(xt, kref, visitados); err != nil {
				return err
			}
		}
	}

	for len(filhos) > ramos {
		var novos types.Array
		var novasContagens []int
		for i := 0; i < len(filhos); i += ramos {
			fim := min(i+ramos, len(filhos))
			total := 0
			for _, c := range contagens[i:fim] {
				total += c
			}
			grupo := append(types.Array{}, filhos[i:fim]...)
			no := types.Dict{
				"Type":   types.Name("Pages"),
				"Parent": ref,
				"Kids":   grupo,
				"Count":  types.Integer(total),
			}
			nref, err := xt.IndRefForNewObject(no)
			if err != nil {
				return err
			}
			for _, o := range grupo {
				if kd, err := xt.DereferenceDict(o); err == nil && kd != nil {
					kd.Update("Parent", *nref)
				}
			}
			novos = append(novos, *nref)
			novasContagens = append(novasContagens, total)
		}
		filhos, contagens = novos, novasContagens
	}
	d.Update("Kids", filhos)
	return nil
}

func ehNoPages(xt *model.XRefTable, ref types.IndirectRef) bool {
	d, err := xt.DereferenceDict(ref)
	if err != nil || d == nil {
		return false
	}
	_, temFilhos := d.Find("Kids")
	return temFilhos
}

// contar devolve quantas páginas existem sob ref (1 para uma página).
func contar(xt *model.XRefTable, ref types.IndirectRef) (int, error) {
	d, err := xt.DereferenceDict(ref)
	if err != nil || d == nil {
		return 0, fmt.Errorf("nó da árvore de páginas inválido (obj %d)", ref.ObjectNumber.Value())
	}
	if !ehNoPages(xt, ref) {
		return 1, nil
	}
	n, err := xt.DereferenceInteger(d["Count"])
	if err != nil || n == nil {
		return 0, fmt.Errorf("nó da árvore de páginas sem Count (obj %d)", ref.ObjectNumber.Value())
	}
	return n.Value(), nil
}

// listarPaginas devolve as páginas na ordem do documento.
func listarPaginas(xt *model.XRefTable) ([]pagina, error) {
	raiz, err := xt.Pages()
	if err != nil || raiz == nil {
		return nil, fmt.Errorf("árvore de páginas ausente: %w", err)
	}
	var pags []pagina
	visitados := map[int]bool{}
	pilha := []types.IndirectRef{*raiz}
	for len(pilha) > 0 {
		ref := pilha[len(pilha)-1]
		pilha = pilha[:len(pilha)-1]
		if visitados[ref.ObjectNumber.Value()] {
			continue
		}
		visitados[ref.ObjectNumber.Value()] = true

		d, err := xt.DereferenceDict(ref)
		if err != nil || d == nil {
			return nil, fmt.Errorf("nó da árvore de páginas inválido (obj %d)", ref.ObjectNumber.Value())
		}
		if !ehNoPages(xt, ref) {
			pags = append(pags, pagina{ref: ref, d: d})
			continue
		}
		filhos, err := xt.DereferenceArray(d["Kids"])
		if err != nil {
			return nil, err
		}
		for i := len(filhos) - 1; i >= 0; i-- { // empilha ao contrário para visitar em ordem
			kref, ok := filhos[i].(types.IndirectRef)
			if !ok {
				return nil, fmt.Errorf("árvore de páginas corrompida")
			}
			pilha = append(pilha, kref)
		}
	}
	return pags, nil
}
