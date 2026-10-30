package divisor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/pdfcpu/pdfcpu/pkg/api"
)

func TestMain(m *testing.M) {
	semReserva = os.Getenv("DIVISOR_RESERVA") == ""
	os.Exit(m.Run())
}

func TestPlanejar(t *testing.T) {
	p := Planejar(`C:\docs\relatorio.pdf`, "saida", 34102, 5000)
	if len(p) != 7 {
		t.Fatalf("esperava 7 partes, veio %d", len(p))
	}
	if ult := p[6]; ult.De != 30001 || ult.Ate != 34102 {
		t.Fatalf("última parte errada: %+v", ult)
	}
	if p := Planejar("a.pdf", "", 10, 5); len(p) != 2 || p[1].Ate != 10 {
		t.Fatalf("divisão exata errada: %+v", p)
	}
	if p := Planejar("a.pdf", "", 3, 5000); len(p) != 1 || p[0].Ate != 3 {
		t.Fatalf("arquivo menor que o limite: %+v", p)
	}
}

func TestPorPaginas(t *testing.T) {
	entrada := gerarPDF(t, 25, func(int) int { return 2000 })
	var ultimo Progresso
	partes, err := Dividir(context.Background(), Opcoes{
		Entrada: entrada, PastaSaida: t.TempDir(), PaginasPorArquivo: 10,
		AoProgredir: func(p Progresso) {
			if p.Fracao < ultimo.Fracao {
				t.Errorf("progresso voltou: %v -> %v", ultimo.Fracao, p.Fracao)
			}
			ultimo = p
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(partes) != 3 || partes[2].De != 21 || partes[2].Ate != 25 {
		t.Fatalf("partes erradas: %+v", partes)
	}
	if ultimo.Fracao != 1 {
		t.Fatalf("progresso terminou em %v", ultimo.Fracao)
	}
	conferirPartes(t, partes, 25)
}

func TestPorTamanho(t *testing.T) {
	// Páginas de tamanhos bem diferentes: a divisão tem de acompanhar o peso real.
	r := rand.New(rand.NewPCG(1, 2))
	tam := make([]int, 120)
	for i := range tam {
		tam[i] = 5000 + r.IntN(60000)
	}
	entrada := gerarPDF(t, len(tam), func(i int) int { return tam[i] })
	st, _ := os.Stat(entrada)

	limite := st.Size() / 5
	partes, err := Dividir(context.Background(), Opcoes{
		Entrada: entrada, PastaSaida: t.TempDir(), BytesPorArquivo: limite,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range partes {
		if p.Bytes > limite || p.Excede {
			t.Errorf("%s tem %d bytes, limite %d", filepath.Base(p.Caminho), p.Bytes, limite)
		}
		if st, _ := os.Stat(p.Caminho); st.Size() != p.Bytes {
			t.Errorf("%s: tamanho informado %d, real %d", p.Caminho, p.Bytes, st.Size())
		}
	}
	if len(partes) > 7 {
		t.Errorf("partes demais (%d) para um limite de 1/5 do arquivo", len(partes))
	}
	conferirPartes(t, partes, len(tam))
}

func TestPaginaMaiorQueLimite(t *testing.T) {
	entrada := gerarPDF(t, 4, func(i int) int { return []int{1000, 200000, 1000, 1000}[i] })
	partes, err := Dividir(context.Background(), Opcoes{
		Entrada: entrada, PastaSaida: t.TempDir(), BytesPorArquivo: 50000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var excede []int
	for _, p := range partes {
		if p.Excede {
			excede = append(excede, p.De)
		}
	}
	if len(excede) != 1 || excede[0] != 2 {
		t.Fatalf("esperava só a página 2 acima do limite: %+v", partes)
	}
	conferirPartes(t, partes, 4)
}

func TestNaoEhPDF(t *testing.T) {
	entrada := filepath.Join(t.TempDir(), "x.pdf")
	os.WriteFile(entrada, []byte("isto não é um pdf"), 0o644)
	_, err := Dividir(context.Background(), Opcoes{Entrada: entrada, PastaSaida: t.TempDir(), PaginasPorArquivo: 1})
	if !errors.Is(err, ErrNaoEhPDF) {
		t.Fatalf("esperava ErrNaoEhPDF, veio %v", err)
	}
}

func TestCancelar(t *testing.T) {
	entrada := gerarPDF(t, 50, func(int) int { return 1000 })
	pasta := t.TempDir()
	c, cancelar := context.WithCancel(context.Background())
	_, err := Dividir(c, Opcoes{
		Entrada: entrada, PastaSaida: pasta, PaginasPorArquivo: 5,
		AoGravar: func(p Parte) {
			if p.De > 10 {
				cancelar()
			}
		},
	})
	if err == nil {
		t.Fatal("esperava erro de cancelamento")
	}
	sobras, _ := filepath.Glob(filepath.Join(pasta, "*.parcial"))
	if len(sobras) > 0 {
		t.Fatalf("arquivos temporários ficaram para trás: %v", sobras)
	}
}

// DIVISOR_PDF=/caminho/arquivo.pdf go test ./divisor -run Real -v
func TestReal(t *testing.T) {
	entrada := os.Getenv("DIVISOR_PDF")
	if entrada == "" {
		t.Skip("defina DIVISOR_PDF")
	}
	for _, o := range []Opcoes{{PaginasPorArquivo: 5000}, {BytesPorArquivo: 100 << 20}} {
		ini := time.Now()
		etapa := Etapa(-1)
		o.Entrada, o.PastaSaida = entrada, t.TempDir()
		o.AoProgredir = func(p Progresso) {
			if p.Etapa != etapa {
				etapa = p.Etapa
				t.Logf("%6.1fs etapa %d (%.0f%%)", time.Since(ini).Seconds(), p.Etapa, p.Fracao*100)
			}
		}
		o.AoGravar = func(p Parte) {
			t.Logf("%6.1fs %s %.1f MB", time.Since(ini).Seconds(), filepath.Base(p.Caminho), float64(p.Bytes)/(1<<20))
		}
		partes, err := Dividir(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("total %.1fs", time.Since(ini).Seconds())
		if os.Getenv("DIVISOR_CONFERIR") != "" {
			conferirPartes(t, partes, partes[len(partes)-1].Ate)
			compararRender(t, entrada, partes)
		}
	}
}

// conferirPartes valida cada arquivo gerado e confere que juntos cobrem
// todas as páginas, em ordem.
func conferirPartes(t *testing.T, partes []Parte, total int) {
	t.Helper()
	prox := 1
	for _, p := range partes {
		if p.De != prox {
			t.Fatalf("buraco ou sobreposição em %+v (esperava começar em %d)", p, prox)
		}
		n, err := api.PageCountFile(context.Background(), p.Caminho)
		if err != nil {
			t.Fatalf("%s inválido: %v", p.Caminho, err)
		}
		if n != p.Ate-p.De+1 {
			t.Fatalf("%s tem %d páginas, esperava %d", p.Caminho, n, p.Ate-p.De+1)
		}
		if qpdf, err := exec.LookPath("qpdf"); err == nil {
			if saida, err := exec.Command(qpdf, "--check", p.Caminho).CombinedOutput(); err != nil {
				t.Fatalf("qpdf --check %s: %v\n%s", p.Caminho, err, saida)
			}
		}
		prox = p.Ate + 1
	}
	if prox != total+1 {
		t.Fatalf("as partes cobrem até a página %d de %d", prox-1, total)
	}
}

// gerarPDF cria um PDF com n páginas; a página i tem uma imagem própria de
// tamBytes(i) bytes (incompressíveis), e todas usam a mesma fonte.
func gerarPDF(t *testing.T, n int, tamBytes func(i int) int) string {
	t.Helper()
	var b bytes.Buffer
	var offs []int
	obj := func(corpo string) {
		offs = append(offs, b.Len())
		fmt.Fprintf(&b, "%d 0 obj\n%s\nendobj\n", len(offs), corpo)
	}
	b.WriteString("%PDF-1.7\n")
	obj("<< /Type /Catalog /Pages 2 0 R >>")
	kids := ""
	for i := range n {
		kids += fmt.Sprintf("%d 0 R ", 4+3*i)
	}
	obj(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", n, kids))
	obj("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>")
	r := rand.New(rand.NewPCG(uint64(n), 7))
	for i := range n {
		ct, im := 5+3*i, 6+3*i
		obj(fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
			"/Resources << /Font << /F1 3 0 R >> /XObject << /Im0 %d 0 R >> >> /Contents %d 0 R "+
			"/Annots [<< /Type /Annot /Subtype /Link /Rect [0 0 50 50] /Dest [4 0 R /Fit] >>] >>", im, ct))
		c := fmt.Sprintf("q 200 0 0 200 100 400 cm /Im0 Do Q BT /F1 24 Tf 72 720 Td (Pagina %d) Tj ET", i+1)
		obj(fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(c), c))
		dados := make([]byte, tamBytes(i))
		for j := range dados {
			dados[j] = byte(r.Uint32())
		}
		obj(fmt.Sprintf("<< /Type /XObject /Subtype /Image /Width %d /Height 1 /ColorSpace /DeviceGray "+
			"/BitsPerComponent 8 /Length %d >>\nstream\n%s\nendstream", len(dados), len(dados), dados))
	}
	xref := b.Len()
	fmt.Fprintf(&b, "xref\n0 %d\n0000000000 65535 f \n", len(offs)+1)
	for _, o := range offs {
		fmt.Fprintf(&b, "%010d 00000 n \n", o)
	}
	fmt.Fprintf(&b, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(offs)+1, xref)

	caminho := filepath.Join(t.TempDir(), "teste.pdf")
	if err := os.WriteFile(caminho, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return caminho
}

// Variantes de estrutura comuns em PDFs reais, geradas com o qpdf.
func TestVariantes(t *testing.T) {
	qpdf, err := exec.LookPath("qpdf")
	if err != nil {
		t.Skip("qpdf não instalado")
	}
	orig := gerarPDF(t, 30, func(i int) int { return 3000 + 100*i })
	variantes := map[string]func(saida string) error{
		"objstm": func(s string) error { return exec.Command(qpdf, "--object-streams=generate", orig, s).Run() },
		"linear": func(s string) error { return exec.Command(qpdf, "--linearize", orig, s).Run() },
		"incremental": func(s string) error {
			b, _ := os.ReadFile(orig)
			// Atualização incremental: redefine o objeto 3 (fonte) e acrescenta uma xref com /Prev.
			prev := bytes.LastIndex(b, []byte("startxref"))
			var n int
			fmt.Sscan(string(b[prev+len("startxref"):]), &n)
			off := len(b)
			b = append(b, []byte("3 0 obj\n<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>\nendobj\n")...)
			x := len(b)
			b = append(b, fmt.Appendf(nil, "xref\n3 1\n%010d 00000 n \ntrailer\n<< /Size 94 /Root 1 0 R /Prev %d >>\nstartxref\n%d\n%%%%EOF\n", off, n, x)...)
			return os.WriteFile(s, b, 0o644)
		},
		"xref-quebrada": func(s string) error {
			b, _ := os.ReadFile(orig)
			i := bytes.LastIndex(b, []byte("startxref"))
			b = append(b[:i], []byte("startxref\n999999\n%%EOF\n")...)
			// Desloca todos os objetos, invalidando as posições da tabela.
			b = append([]byte("%PDF-1.7\n% lixo inserido no começo\n"), b[9:]...)
			return os.WriteFile(s, b, 0o644)
		},
	}
	for nome, gerar := range variantes {
		t.Run(nome, func(t *testing.T) {
			entrada := filepath.Join(t.TempDir(), nome+".pdf")
			if err := gerar(entrada); err != nil {
				t.Fatal(err)
			}
			for _, o := range []Opcoes{{PaginasPorArquivo: 7}, {BytesPorArquivo: 30000}} {
				o.Entrada, o.PastaSaida = entrada, t.TempDir()
				partes, err := Dividir(context.Background(), o)
				if err != nil {
					t.Fatal(err)
				}
				conferirPartes(t, partes, 30)
			}
		})
	}
}

// compararRender renderiza a primeira, a do meio e a última página de cada
// parte e confere que são idênticas às páginas correspondentes do original.
func compararRender(t *testing.T, entrada string, partes []Parte) {
	gs, err := exec.LookPath("gs")
	if err != nil {
		t.Log("gs não instalado: comparação visual pulada")
		return
	}
	render := func(pdf string, pag int) []byte {
		saida, err := exec.Command(gs, "-q", "-dNOPAUSE", "-dBATCH", "-dSAFER", "-sDEVICE=png16m", "-r40",
			fmt.Sprintf("-dFirstPage=%d", pag), fmt.Sprintf("-dLastPage=%d", pag), "-sOutputFile=-", pdf).Output()
		if err != nil {
			t.Fatalf("gs %s p%d: %v", pdf, pag, err)
		}
		return saida
	}
	for _, p := range partes {
		for _, pag := range []int{p.De, (p.De + p.Ate) / 2, p.Ate} {
			if !bytes.Equal(render(entrada, pag), render(p.Caminho, pag-p.De+1)) {
				t.Errorf("página %d difere em %s", pag, filepath.Base(p.Caminho))
			}
		}
	}
	t.Logf("render conferido em %d páginas", 3*len(partes))
}

func TestMotorPdfcpu(t *testing.T) {
	entrada := gerarPDF(t, 40, func(i int) int { return 2000 + 500*(i%7) })
	for _, o := range []Opcoes{{PaginasPorArquivo: 15}, {BytesPorArquivo: 40000}} {
		f, err := os.Open(entrada)
		if err != nil {
			t.Fatal(err)
		}
		em := &emissor{}
		m, err := novoMotorPdfcpu(context.Background(), f, em)
		if err != nil {
			t.Fatal(err)
		}
		o.PastaSaida = t.TempDir()
		d := &divisao{c: context.Background(), o: o, em: em, base: "x", tamEntrada: 200000, fimPreparo: pdfcpuFimPreparo}
		d.o.AoGravar = func(Parte) {}
		partes, err := d.executar(m)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		conferirPartes(t, partes, 40)
	}
}
