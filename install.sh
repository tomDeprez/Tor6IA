#!/bin/sh
# Tor6IA installer for Linux and macOS: one command installs everything and starts it.
#   curl -fsSL https://raw.githubusercontent.com/tomDeprez/Tor6IA/main/install.sh | sh
# Options, as environment variables:
#   T6_COORDINATOR=https://your.network   join that network instead of the default one
#   T6_NO_START=1                         install only, do not start
set -eu

# The GitHub repository that publishes the releases.
REPO="tomDeprez/Tor6IA"

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$(uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  arm64 | aarch64) arch=arm64 ;;
  *) echo "Unsupported processor: $(uname -m)" >&2; exit 1 ;;
esac
case "$os" in
  linux | darwin) ;;
  *) echo "Unsupported system: $os. On Windows, download tor6ia-windows-amd64.exe from https://github.com/$REPO/releases/latest" >&2; exit 1 ;;
esac

# Ollama runs the models. Install it when it is missing.
if ! command -v ollama >/dev/null 2>&1 && [ ! -d /Applications/Ollama.app ]; then
  echo "Installing Ollama..."
  if [ "$os" = linux ]; then
    curl -fsSL https://ollama.com/install.sh | sh # asks for your password
  elif command -v brew >/dev/null 2>&1; then
    brew install ollama
  else
    tmp=$(mktemp -d)
    curl -fL https://ollama.com/download/Ollama-darwin.zip -o "$tmp/Ollama.zip"
    unzip -q "$tmp/Ollama.zip" -d /Applications
    rm -rf "$tmp"
  fi
fi
# The macOS app has no command in the PATH: start it so that its server is up for Tor6IA.
if [ "$os" = darwin ] && ! command -v ollama >/dev/null 2>&1; then
  open -a Ollama || true
fi

bin="$HOME/.local/bin"
mkdir -p "$bin"
echo "Downloading Tor6IA..."
curl -fL "https://github.com/$REPO/releases/latest/download/tor6ia-$os-$arch" -o "$bin/tor6ia.new"
chmod +x "$bin/tor6ia.new"
mv -f "$bin/tor6ia.new" "$bin/tor6ia"
echo "Tor6IA is installed: $bin/tor6ia"

if [ -n "${T6_NO_START:-}" ]; then
  exit 0
fi
echo "Starting Tor6IA: your browser opens on http://localhost:7606"
if [ -n "${T6_COORDINATOR:-}" ]; then
  exec "$bin/tor6ia" start --coordinator "$T6_COORDINATOR"
fi
exec "$bin/tor6ia" start
