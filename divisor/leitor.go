package divisor

import (
	"errors"
	"io"
	"os"
)

const (
	tamBloco    = 1 << 20 // leituras do disco em blocos de 1 MB
	blocosCache = 32      // blocos mantidos em memória
)

// leitor entrega o arquivo ao pdfcpu a partir de blocos grandes em cache.
// O pdfcpu faz centenas de milhares de leituras pequenas e sobrepostas (um
// PDF de 700 MB vira ~320 mil leituras e 1,8 GB lidos); no Windows cada uma
// é uma chamada ao sistema, inspecionada pelo antivírus. Com o cache, o mesmo
// arquivo é lido em ~700 blocos.
//
// Também serve de medidor de progresso: conta quantos blocos distintos do
// arquivo já foram tocados.
type leitor struct {
	f      *os.File
	tam    int64
	pos    int64
	cache  map[int64][]byte
	ordem  []int64 // blocos em cache, do mais antigo ao mais recente
	vistos []bool
	nVisto int
	aoLer  func(fracao float64)
}

func novoLeitor(f *os.File, aoLer func(float64)) (*leitor, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	n := (st.Size() + tamBloco - 1) / tamBloco
	return &leitor{
		f: f, tam: st.Size(), aoLer: aoLer,
		cache: make(map[int64][]byte, blocosCache), vistos: make([]bool, n),
	}, nil
}

func (l *leitor) Read(p []byte) (int, error) {
	if l.pos >= l.tam {
		return 0, io.EOF
	}
	lidos := 0
	for lidos < len(p) && l.pos < l.tam {
		b, err := l.bloco(l.pos / tamBloco)
		if err != nil {
			return lidos, err
		}
		n := copy(p[lidos:], b[l.pos%tamBloco:])
		lidos += n
		l.pos += int64(n)
	}
	return lidos, nil
}

func (l *leitor) Seek(desloc int64, de int) (int64, error) {
	switch de {
	case io.SeekCurrent:
		desloc += l.pos
	case io.SeekEnd:
		desloc += l.tam
	}
	if desloc < 0 {
		return 0, errors.New("posição negativa")
	}
	l.pos = desloc
	return desloc, nil
}

func (l *leitor) bloco(i int64) ([]byte, error) {
	if b, ok := l.cache[i]; ok {
		return b, nil
	}
	var b []byte
	if len(l.cache) >= blocosCache { // reaproveita o buffer do bloco mais antigo
		velho := l.ordem[0]
		l.ordem = l.ordem[1:]
		b = l.cache[velho][:cap(l.cache[velho])]
		delete(l.cache, velho)
	} else {
		b = make([]byte, tamBloco)
	}
	n, err := l.f.ReadAt(b, i*tamBloco)
	if err != nil && !(errors.Is(err, io.EOF) && n > 0) {
		return nil, err
	}
	b = b[:n]
	l.cache[i] = b
	l.ordem = append(l.ordem, i)

	if !l.vistos[i] {
		l.vistos[i] = true
		l.nVisto++
		if l.aoLer != nil {
			l.aoLer(float64(l.nVisto) / float64(len(l.vistos)))
		}
	}
	return b, nil
}
