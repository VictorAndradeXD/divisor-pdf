package divisor

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strconv"
)

// Objetos PDF lidos pelo motor de cópia direta. Números, nomes e strings
// guardam o texto original, para que um objeto reescrito saia idêntico.
type (
	objeto     any
	nome       string // sem a barra; escapes #xx preservados
	numero     string
	textoBruto []byte // string literal ou hexadecimal, com os delimitadores
	palavra    string // true, false, null
	array      []objeto
	dicionario map[nome]objeto
	ref        struct{ num, ger int }
)

func (n numero) inteiro() (int64, bool) {
	v, err := strconv.ParseInt(string(n), 10, 64)
	return v, err == nil
}

// inteiro devolve o valor de um número inteiro, ou 0.
func inteiro(o objeto) int64 {
	n, _ := o.(numero)
	v, _ := n.inteiro()
	return v
}

func (d dicionario) nome(chave nome) nome {
	n, _ := d[chave].(nome)
	return n
}

// janela dá acesso aos bytes de onde os objetos são lidos: o arquivo ou um
// object stream já descomprimido.
type janela interface {
	byteEm(pos int64) (byte, bool)
}

type memoria []byte

func (m memoria) byteEm(pos int64) (byte, bool) {
	if pos < 0 || pos >= int64(len(m)) {
		return 0, false
	}
	return m[pos], true
}

var errFimInesperado = errors.New("fim inesperado dos dados")

type lexico struct {
	j   janela
	pos int64
}

func ehEspaco(b byte) bool {
	return b == ' ' || b == '\n' || b == '\r' || b == '\t' || b == '\f' || b == 0
}

func ehDelimitador(b byte) bool {
	return bytes.IndexByte([]byte("()<>[]{}/%"), b) >= 0
}

func (l *lexico) espiar() (byte, bool) { return l.j.byteEm(l.pos) }

// pularEspacos ignora espaços e comentários.
func (l *lexico) pularEspacos() {
	for {
		b, ok := l.espiar()
		switch {
		case !ok:
			return
		case ehEspaco(b):
			l.pos++
		case b == '%':
			for ok && b != '\n' && b != '\r' {
				l.pos++
				b, ok = l.espiar()
			}
		default:
			return
		}
	}
}

// palavraCrua lê uma sequência de caracteres regulares (número ou palavra-chave).
func (l *lexico) palavraCrua() string {
	var s []byte
	for {
		b, ok := l.espiar()
		if !ok || ehEspaco(b) || ehDelimitador(b) {
			return string(s)
		}
		s = append(s, b)
		l.pos++
	}
}

// valor lê o próximo objeto direto (uma referência "n g R" também).
func (l *lexico) valor() (objeto, error) {
	return l.valorProf(0)
}

func (l *lexico) valorProf(prof int) (objeto, error) {
	if prof > 200 {
		return nil, errors.New("objeto aninhado demais")
	}
	l.pularEspacos()
	b, ok := l.espiar()
	if !ok {
		return nil, errFimInesperado
	}
	switch {
	case b == '/':
		l.pos++
		return nome(l.palavraCrua()), nil
	case b == '(':
		return l.literal()
	case b == '<':
		if b2, _ := l.j.byteEm(l.pos + 1); b2 == '<' {
			return l.dicionario(prof)
		}
		return l.hexa()
	case b == '[':
		l.pos++
		var a array
		for {
			l.pularEspacos()
			b, ok := l.espiar()
			if !ok {
				return nil, errFimInesperado
			}
			if b == ']' {
				l.pos++
				return a, nil
			}
			v, err := l.valorProf(prof + 1)
			if err != nil {
				return nil, err
			}
			a = append(a, v)
		}
	case b == '+' || b == '-' || b == '.' || (b >= '0' && b <= '9'):
		n := l.palavraCrua()
		if _, err := strconv.ParseFloat(n, 64); err != nil {
			if n == "-" || n == "+" || n == "." { // números malformados comuns: valem 0
				return numero("0"), nil
			}
			return nil, fmt.Errorf("número inválido %q", n)
		}
		if r, ok := l.talvezRef(n); ok {
			return r, nil
		}
		return numero(n), nil
	default:
		p := l.palavraCrua()
		if p == "" {
			return nil, fmt.Errorf("caractere inesperado %q na posição %d", b, l.pos)
		}
		return palavra(p), nil
	}
}

// talvezRef reconhece "n g R" depois de ter lido o inteiro n.
func (l *lexico) talvezRef(n string) (ref, bool) {
	num, err := strconv.Atoi(n)
	if err != nil || num < 0 {
		return ref{}, false
	}
	volta := l.pos
	l.pularEspacos()
	g := l.palavraCrua()
	ger, err := strconv.Atoi(g)
	if err == nil && ger >= 0 {
		l.pularEspacos()
		if b, ok := l.espiar(); ok && b == 'R' {
			if b2, ok := l.j.byteEm(l.pos + 1); !ok || ehEspaco(b2) || ehDelimitador(b2) {
				l.pos++
				return ref{num, ger}, true
			}
		}
	}
	l.pos = volta
	return ref{}, false
}

func (l *lexico) dicionario(prof int) (objeto, error) {
	l.pos += 2
	d := dicionario{}
	for {
		l.pularEspacos()
		b, ok := l.espiar()
		if !ok {
			return nil, errFimInesperado
		}
		if b == '>' {
			l.pos += 2
			return d, nil
		}
		if b != '/' {
			// Lixo onde deveria haver uma chave: ignora o token.
			if _, err := l.valorProf(prof + 1); err != nil {
				return nil, err
			}
			continue
		}
		l.pos++
		k := nome(l.palavraCrua())
		l.pularEspacos()
		if b, _ := l.espiar(); b == '>' { // chave sem valor
			continue
		}
		v, err := l.valorProf(prof + 1)
		if err != nil {
			return nil, err
		}
		if p, ok := v.(palavra); ok && p == "null" {
			continue // chave com null equivale a chave ausente
		}
		d[k] = v
	}
}

func (l *lexico) literal() (objeto, error) {
	var s []byte
	nivel := 0
	for {
		b, ok := l.espiar()
		if !ok {
			return nil, errFimInesperado
		}
		s = append(s, b)
		l.pos++
		switch b {
		case '\\':
			if b2, ok := l.espiar(); ok {
				s = append(s, b2)
				l.pos++
			}
		case '(':
			nivel++
		case ')':
			nivel--
			if nivel == 0 {
				return textoBruto(s), nil
			}
		}
	}
}

func (l *lexico) hexa() (objeto, error) {
	var s []byte
	for {
		b, ok := l.espiar()
		if !ok {
			return nil, errFimInesperado
		}
		s = append(s, b)
		l.pos++
		if b == '>' {
			return textoBruto(s), nil
		}
	}
}

// escrever serializa o objeto no formato PDF.
func escrever(buf *bytes.Buffer, o objeto) {
	switch o := o.(type) {
	case nil:
		buf.WriteString("null")
	case nome:
		buf.WriteByte('/')
		buf.WriteString(string(o))
	case numero:
		buf.WriteString(string(o))
	case textoBruto:
		buf.Write(o)
	case palavra:
		buf.WriteString(string(o))
	case ref:
		fmt.Fprintf(buf, "%d %d R", o.num, o.ger)
	case array:
		buf.WriteByte('[')
		for i, v := range o {
			if i > 0 {
				buf.WriteByte(' ')
			}
			escrever(buf, v)
		}
		buf.WriteByte(']')
	case dicionario:
		buf.WriteString("<<")
		chaves := make([]nome, 0, len(o))
		for k := range o {
			chaves = append(chaves, k)
		}
		slices.Sort(chaves)
		for _, k := range chaves {
			buf.WriteByte('/')
			buf.WriteString(string(k))
			buf.WriteByte(' ')
			escrever(buf, o[k])
		}
		buf.WriteString(">>")
	default:
		panic(fmt.Sprintf("tipo de objeto desconhecido %T", o))
	}
}
