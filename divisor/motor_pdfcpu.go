package divisor

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

// Acima disto o motor completo não é tentado: ele carrega o PDF inteiro na
// memória (cerca de 3,5 vezes o tamanho do arquivo).
const limitePdfcpu = 1 << 30

// motorPdfcpu é o motor completo, usado quando o de cópia direta não
// consegue tratar o arquivo (PDFs criptografados com object streams, por
// exemplo). Lê e valida o PDF inteiro com o pdfcpu.
type motorPdfcpu struct {
	ctx  *model.Context
	pags []pagina
	med  *medidor
}

type pagina struct {
	ref types.IndirectRef
	d   types.Dict
}

// Etapas do motor completo na barra de progresso, medidas num PDF de 700 MB.
const (
	pdfcpuFimLeitura     = 0.30
	pdfcpuFimVerificacao = 0.38
	pdfcpuFimPreparo     = 0.40
	pdfcpuFracExtracao   = 0.6 // dentro de cada parte: copiar páginas; o resto é gravar
)

func novoMotorPdfcpu(c context.Context, f *os.File, em *emissor) (*motorPdfcpu, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	l, err := novoLeitor(f, func(fr float64) { em.enviar(Progresso{Etapa: Lendo, Fracao: fr * pdfcpuFimLeitura}, false) })
	if err != nil {
		return nil, err
	}

	conf := model.NewStatelessConfiguration() // não grava config.yml no perfil do usuário
	conf.Cmd = model.SPLIT
	conf.ValidationMode = model.ValidationRelaxed
	conf.Optimize = false // otimizar o arquivo inteiro antes de dividir é lento e desnecessário

	ctx, err := api.ReadValidateAndOptimize(c, l, conf, &api.ProgressOptions{
		Observer: func(ev api.ProgressEvent) error {
			if ev.Stage == api.ProgressStageValidating {
				em.enviar(Progresso{Etapa: Verificando, Fracao: pdfcpuFimLeitura}, true)
			}
			return nil
		},
	})
	if err != nil {
		return nil, fmt.Errorf("não foi possível ler o PDF: %w", err)
	}

	em.enviar(Progresso{Etapa: Preparando, Fracao: pdfcpuFimVerificacao, Paginas: ctx.PageCount}, true)
	if err := balancear(ctx.XRefTable); err != nil {
		return nil, fmt.Errorf("não foi possível preparar o PDF: %w", err)
	}
	pags, err := listarPaginas(ctx.XRefTable)
	if err != nil {
		return nil, fmt.Errorf("não foi possível preparar o PDF: %w", err)
	}
	if len(pags) != ctx.PageCount {
		return nil, fmt.Errorf("a árvore de páginas tem %d páginas, mas o PDF declara %d", len(pags), ctx.PageCount)
	}
	return &motorPdfcpu{ctx: ctx, pags: pags, med: novoMedidor(ctx.XRefTable)}, nil
}

func (m *motorPdfcpu) totalPaginas() int { return len(m.pags) }

func (m *motorPdfcpu) novoArquivo() error { m.med.novoArquivo(); return nil }

func (m *motorPdfcpu) custo(i int) (int64, error) {
	return m.med.pagina(m.pags[i-1].d, m.pags[i-1].ref), nil
}

func (m *motorPdfcpu) gravar(c context.Context, de, ate int, w io.Writer, sub func(float64)) error {
	novo, err := pdfcpu.ExtractPages(c, m.ctx, paginas(de, ate), false)
	if err != nil {
		return fmt.Errorf("erro ao copiar as páginas %d a %d: %w", de, ate, err)
	}
	sub(pdfcpuFracExtracao)
	return api.WriteContext(c, novo, w)
}

func paginas(de, ate int) []int {
	p := make([]int, 0, ate-de+1)
	for i := de; i <= ate; i++ {
		p = append(p, i)
	}
	return p
}
