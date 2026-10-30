#!/usr/bin/env bash
# Gera o DivisorPDF.exe (Windows 64 bits) e o divisor-pdf (Linux/Wayland).
#   ./build.sh            os dois
#   ./build.sh windows    só o .exe
#   ./build.sh linux      só o Linux
set -euo pipefail
cd "$(dirname "$0")"
alvo="${1:-todos}"

if [[ $alvo == todos || $alvo == windows ]]; then
	# Go puro: compila a partir do Linux sem compilador C.
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
		go build -trimpath -ldflags "-s -w -H=windowsgui" -o DivisorPDF.exe .
	echo "Gerado: $(pwd)/DivisorPDF.exe"
fi

if [[ $alvo == todos || $alvo == linux ]]; then
	# No Linux o Gio usa cgo (Wayland/X11 + OpenGL). "novulkan" dispensa os
	# cabeçalhos do Vulkan, que não vêm instalados por padrão.
	CGO_ENABLED=1 go build -tags novulkan -trimpath -ldflags "-s -w" -o divisor-pdf .
	echo "Gerado: $(pwd)/divisor-pdf"
fi
