// Package divisor divide um PDF em vários arquivos menores, por número de
// páginas ou por tamanho máximo de arquivo.
//
// O trabalho é feito pelo motor de cópia direta (motor_direto.go), que copia
// os objetos de cada parte do arquivo original sem carregá-lo na memória e
// por isso aguenta PDFs de vários gigabytes. Arquivos que ele não trata vão
// para o motor completo, baseado no pdfcpu (motor_pdfcpu.go).
package divisor

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
)

// Opcoes descreve uma divisão. Exatamente um dos limites deve ser positivo.
type Opcoes struct {
	Entrada    string
	PastaSaida string

	PaginasPorArquivo int   // divide a cada N páginas
	BytesPorArquivo   int64 // ou: cada arquivo com no máximo N bytes

	// Chamadas a partir da goroutine que executa Dividir; podem ser nil.
	AoProgredir func(Progresso)
	AoGravar    func(Parte)
}

// Etapa da divisão.
type Etapa int

const (
	Lendo       Etapa = iota // lendo o arquivo do disco
	Verificando              // conferindo (ou reconstruindo) a estrutura do PDF
	Preparando               // organizando as páginas
	Gravando                 // gravando as partes
)

// Progresso informa o andamento da divisão.
type Progresso struct {
	Etapa   Etapa
	Fracao  float64 // andamento total, de 0 a 1; nunca diminui
	Paginas int     // total de páginas do PDF (0 enquanto ele é lido)
	Parte   int     // número da parte em gravação, a partir de 1
	De, Ate int     // páginas da parte em gravação
}

// Parte descreve um arquivo de saída.
type Parte struct {
	Caminho string
	De, Ate int
	Bytes   int64
	Excede  bool // passou do limite de tamanho: uma única página já é maior que ele
}

// ErrNaoEhPDF indica que o arquivo de entrada não começa como um PDF.
var ErrNaoEhPDF = errors.New("o arquivo não é um PDF")

// ErroInterno é uma falha inesperada ao processar o PDF (um panic), com a
// pilha de chamadas para diagnóstico.
type ErroInterno struct {
	Valor any
	Pilha []byte
}

func (e *ErroInterno) Error() string {
	return fmt.Sprintf("falha interna ao processar o PDF: %v", e.Valor)
}

// motor lê o PDF de entrada e grava partes dele.
type motor interface {
	totalPaginas() int
	// novoArquivo começa o planejamento de uma parte; custo inclui nela a
	// página i (chamada em ordem) e devolve quantos bytes ela acrescenta.
	novoArquivo() error
	custo(i int) (int64, error)
	// gravar escreve as páginas de..ate planejadas; sub informa o andamento
	// de etapas internas que não escrevem bytes.
	gravar(c context.Context, de, ate int, w io.Writer, sub func(float64)) error
}

// Etapas do motor de cópia direta na barra de progresso: ler a xref e
// listar as páginas é rápido; quase todo o tempo vai em copiar os dados.
const (
	diretoFimLeitura = 0.02
	diretoFimPreparo = 0.05
)

// semReserva desliga o motor completo (nos testes do motor direto).
var semReserva bool

// Margem sob o limite de tamanho, para absorver o erro da estimativa.
const folga = 0.98

// Dividir lê o PDF e grava as partes em o.PastaSaida.
// Retorna as partes gravadas, mesmo em caso de erro ou cancelamento.
func Dividir(c context.Context, o Opcoes) (partes []Parte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = &ErroInterno{Valor: r, Pilha: debug.Stack()}
		}
	}()

	if (o.PaginasPorArquivo > 0) == (o.BytesPorArquivo > 0) {
		return nil, errors.New("informe o limite de páginas ou o de tamanho por arquivo")
	}
	em := &emissor{f: o.AoProgredir}
	if o.AoGravar == nil {
		o.AoGravar = func(Parte) {}
	}

	f, err := os.Open(o.Entrada)
	if err != nil {
		return nil, fmt.Errorf("não foi possível abrir o arquivo: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.PastaSaida, 0o755); err != nil {
		return nil, fmt.Errorf("não foi possível criar a pasta de saída: %w", err)
	}

	d := &divisao{c: c, o: o, em: em, tamEntrada: st.Size(),
		base: strings.TrimSuffix(filepath.Base(o.Entrada), filepath.Ext(o.Entrada))}

	em.enviar(Progresso{Etapa: Lendo}, true)
	partes, err = d.direto(f)
	if errDireto := err; errDireto != nil && !errors.Is(err, ErrNaoEhPDF) && c.Err() == nil && st.Size() <= limitePdfcpu && !semReserva {
		// O motor completo recomeça do zero; as partes já gravadas são refeitas.
		em.maior = 0
		var m *motorPdfcpu
		if m, err = novoMotorPdfcpu(c, f, em); err != nil {
			return nil, fmt.Errorf("%w (leitura direta: %v)", err, errDireto)
		}
		d.fimPreparo = pdfcpuFimPreparo
		return d.executar(m)
	}
	return partes, err
}

func (d *divisao) direto(f *os.File) ([]Parte, error) {
	a, err := abrirPDF(f)
	if err != nil {
		return nil, err
	}
	if a.reparado {
		d.em.enviar(Progresso{Etapa: Verificando, Fracao: diretoFimLeitura}, true)
	}
	d.em.enviar(Progresso{Etapa: Preparando, Fracao: diretoFimLeitura}, true)
	m, err := novoMotorDireto(a, func(feitas, total int) {
		if total > 0 {
			fr := diretoFimLeitura + (diretoFimPreparo-diretoFimLeitura)*float64(feitas)/float64(total)
			d.em.enviar(Progresso{Etapa: Preparando, Fracao: fr, Paginas: total}, false)
		}
	})
	if err != nil {
		return nil, err
	}
	d.fimPreparo = diretoFimPreparo
	return d.executar(m)
}

type divisao struct {
	c          context.Context
	o          Opcoes
	em         *emissor
	tamEntrada int64
	base       string
	fimPreparo float64

	somaReal, somaEst int64 // calibração da estimativa de tamanho
}

func (d *divisao) executar(m motor) ([]Parte, error) {
	total := m.totalPaginas()
	d.em.enviar(Progresso{Etapa: Preparando, Fracao: d.fimPreparo, Paginas: total}, true)
	var digitos int
	if d.o.PaginasPorArquivo > 0 {
		digitos = len(fmt.Sprint((total + d.o.PaginasPorArquivo - 1) / d.o.PaginasPorArquivo))
	} else {
		n := d.tamEntrada/d.o.BytesPorArquivo + 1
		digitos = len(fmt.Sprint(n + n/4))
	}
	digitos = max(digitos, 2)

	var partes []Parte
	aperto := 1.0 // reduzido quando uma parte passa do limite e é refeita
	for de := 1; de <= total; {
		ate, est, err := d.planejar(m, de, aperto)
		if err != nil {
			return partes, err
		}
		nome := nomeParte(d.base, digitos, len(partes)+1, de, ate)
		p := Parte{Caminho: filepath.Join(d.o.PastaSaida, nome), De: de, Ate: ate}
		temp, n, err := d.gravar(m, len(partes)+1, de, ate, est, p.Caminho)
		if err != nil {
			return partes, err
		}
		p.Bytes = n

		if lim := d.o.BytesPorArquivo; lim > 0 && n > lim {
			if ate > de && aperto > 0.05 {
				os.Remove(temp)
				aperto *= float64(lim) / float64(n) * folga
				continue
			}
			p.Excede = true
		}
		if err := substituir(temp, p.Caminho); err != nil {
			return partes, err
		}
		d.somaReal += n
		d.somaEst += est
		aperto = 1
		partes = append(partes, p)
		d.o.AoGravar(p)
		de = ate + 1
	}
	d.em.enviar(Progresso{Etapa: Gravando, Fracao: 1, Paginas: total, Parte: len(partes)}, true)
	return partes, nil
}

// planejar escolhe a última página da parte que começa em de e estima seu
// tamanho em bytes.
func (d *divisao) planejar(m motor, de int, aperto float64) (ate int, est int64, err error) {
	if err := m.novoArquivo(); err != nil {
		return 0, 0, err
	}
	est = 4 << 10
	total := m.totalPaginas()
	if d.o.PaginasPorArquivo > 0 {
		ate = min(de+d.o.PaginasPorArquivo-1, total)
		for i := de; i <= ate; i++ {
			c, err := m.custo(i)
			if err != nil {
				return 0, 0, err
			}
			est += c
		}
		return ate, est, nil
	}

	razao := 1.0 // bytes gravados por byte estimado
	if d.somaEst > 0 {
		razao = float64(d.somaReal) / float64(d.somaEst)
	}
	alvo := float64(d.o.BytesPorArquivo) * folga * aperto / razao
	ate = de
	for i := de; i <= total; i++ {
		c, err := m.custo(i)
		if err != nil {
			return 0, 0, err
		}
		if i > de && float64(est+c) > alvo {
			break
		}
		est += c
		ate = i
	}
	return ate, est, nil
}

// gravar escreve as páginas de..ate num arquivo temporário ao lado de
// destino e devolve o caminho dele e o tamanho gravado.
func (d *divisao) gravar(m motor, parte, de, ate int, est int64, destino string) (string, int64, error) {
	total := m.totalPaginas()
	base := 0.0
	prog := func(sub float64, forcar bool) {
		feitas := float64(de-1) + float64(ate-de+1)*min(sub, 1)
		d.em.enviar(Progresso{
			Etapa: Gravando, Paginas: total, Parte: parte, De: de, Ate: ate,
			Fracao: d.fimPreparo + (1-d.fimPreparo)*feitas/float64(total),
		}, forcar)
	}
	prog(0, true)

	temp := destino + ".parcial"
	f, err := os.Create(temp)
	if err != nil {
		return "", 0, fmt.Errorf("não foi possível criar %s: %w", filepath.Base(temp), err)
	}
	buf := bufio.NewWriterSize(f, 1<<20)
	cont := &contador{w: buf, aoEscrever: func(n int64) {
		prog(base+(1-base)*float64(n)/float64(max(est, 1)), false)
	}}
	err = m.gravar(d.c, de, ate, cont, func(s float64) { base = s; prog(s, false) })
	if err == nil {
		err = buf.Flush()
	}
	if errFechar := f.Close(); err == nil {
		err = errFechar
	}
	if err != nil {
		os.Remove(temp)
		if d.c.Err() != nil {
			return "", 0, d.c.Err()
		}
		return "", 0, fmt.Errorf("erro ao gravar %s: %w", filepath.Base(destino), err)
	}
	return temp, cont.n, nil
}

func substituir(temp, destino string) error {
	if err := os.Rename(temp, destino); err != nil {
		os.Remove(temp)
		return fmt.Errorf("não foi possível gravar %s: %w", filepath.Base(destino), err)
	}
	return nil
}

// Planejar calcula os nomes e intervalos de páginas de cada arquivo de saída
// na divisão por número de páginas.
func Planejar(entrada, pastaSaida string, totalPaginas, porArquivo int) []Parte {
	base := strings.TrimSuffix(filepath.Base(entrada), filepath.Ext(entrada))
	n := (totalPaginas + porArquivo - 1) / porArquivo
	digitos := max(len(fmt.Sprint(n)), 2)

	partes := make([]Parte, 0, n)
	for i := 0; i < n; i++ {
		de := i*porArquivo + 1
		ate := min(de+porArquivo-1, totalPaginas)
		nome := nomeParte(base, digitos, i+1, de, ate)
		partes = append(partes, Parte{Caminho: filepath.Join(pastaSaida, nome), De: de, Ate: ate})
	}
	return partes
}

// nomeParte gera "<nome>_parte01_p1-5000.pdf".
func nomeParte(base string, digitos, i, de, ate int) string {
	return fmt.Sprintf("%s_parte%0*d_p%d-%d.pdf", base, digitos, i, de, ate)
}

// contador repassa a escrita e informa quantos bytes já foram gravados.
type contador struct {
	w          io.Writer
	n          int64
	aoEscrever func(int64)
}

func (c *contador) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	c.aoEscrever(c.n)
	return n, err
}

// emissor limita a frequência dos avisos de progresso e impede que a fração
// volte atrás (quando uma parte grande demais é refeita, por exemplo).
type emissor struct {
	f      func(Progresso)
	ultimo time.Time
	maior  float64
}

func (e *emissor) enviar(p Progresso, forcar bool) {
	if e.f == nil {
		return
	}
	e.maior = max(e.maior, p.Fracao)
	p.Fracao = e.maior
	if !forcar && time.Since(e.ultimo) < 50*time.Millisecond {
		return
	}
	e.ultimo = time.Now()
	e.f(p)
}
