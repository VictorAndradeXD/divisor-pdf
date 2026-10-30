#!/usr/bin/env bash
# Instala o Divisor de PDF no Omarchy (ou outro Linux): binário em
# ~/.local/bin e atalho no menu de aplicativos (Super + Espaço).
set -euo pipefail
cd "$(dirname "$0")"

[[ -x divisor-pdf ]] || ./build.sh linux

mkdir -p ~/.local/bin ~/.local/share/applications
install -m 755 divisor-pdf ~/.local/bin/divisor-pdf

cat > ~/.local/share/applications/divisor-pdf.desktop <<DESKTOP
[Desktop Entry]
Type=Application
Name=Divisor de PDF
Comment=Divide PDFs enormes em arquivos menores, por páginas ou por tamanho
Exec=$HOME/.local/bin/divisor-pdf %f
Icon=application-pdf
Terminal=false
Categories=Office;Utility;
MimeType=application/pdf;
StartupWMClass=divisor-pdf
DESKTOP

update-desktop-database ~/.local/share/applications 2>/dev/null || true
echo "Instalado: ~/.local/bin/divisor-pdf"
echo "Abra pelo menu (Super + Espaço → Divisor de PDF) ou com: divisor-pdf arquivo.pdf"
echo
echo "Dica: para a janela abrir flutuando no Hyprland, adicione ao seu hyprland.conf:"
echo "  windowrule = float, class:^(divisor-pdf)\$"
