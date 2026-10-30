package main

import (
	"fmt"
	"strings"
	"time"
)

const mb = 1 << 20 // o "MB" que o Explorer do Windows mostra

// milhar formata 34102 como "34.102".
func milhar(n int) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + milhar(-n)
	}
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte('.')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// tamanho formata bytes como "698,4 MB".
func tamanho(b int64) string {
	switch {
	case b < 1<<10:
		return fmt.Sprintf("%d bytes", b)
	case b < mb:
		return fmt.Sprintf("%d KB", (b+1<<9)>>10)
	case b < 1<<30:
		return decimal(float64(b)/mb) + " MB"
	default:
		return decimal(float64(b)/(1<<30)) + " GB"
	}
}

func decimal(v float64) string {
	if v >= 100 {
		return milhar(int(v + 0.5))
	}
	return strings.Replace(fmt.Sprintf("%.1f", v), ".", ",", 1)
}

// duracao formata como "45 s", "1 min 12 s" ou "1 h 03 min".
func duracao(d time.Duration) string {
	s := int(d.Round(time.Second).Seconds())
	switch {
	case s < 60:
		return fmt.Sprintf("%d s", s)
	case s < 3600:
		return fmt.Sprintf("%d min %02d s", s/60, s%60)
	default:
		return fmt.Sprintf("%d h %02d min", s/3600, s/60%60)
	}
}

// plural escolhe a forma de acordo com n: plural(2, "arquivo", "arquivos").
func plural(n int, um, varios string) string {
	if n == 1 {
		return um
	}
	return varios
}
