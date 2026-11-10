#!/usr/bin/env bash
# Build Shocking VRChat (Go + WebView2).
#   ./build.sh            -> dist/shocking_vrchat_go_windows_x64/ + zip
#   ./build.sh linux      -> dist/shocking_vrchat (native, browser UI)
#   SKIP_FRONTEND=1 ./build.sh   reuse assets/static
set -euo pipefail
cd "$(dirname "$0")"

VERSION="${VERSION:-0.7.14}"
LDFLAGS="-s -w -X shockingvrc/internal/app.Version=${VERSION}"
if [[ -n "${GITHUB_REPO:-}" ]]; then
  LDFLAGS="$LDFLAGS -X shockingvrc/internal/app.GitHubRepo=${GITHUB_REPO}"
fi

if [[ "${SKIP_FRONTEND:-0}" != "1" ]]; then
  echo ">> building frontend"
  (cd frontend && { [[ -d node_modules ]] || npm ci --no-audit --no-fund; } && npm run build)
fi

echo ">> packing default presets"
go run ./tools/packassets

echo ">> running tests"
go test ./...

mkdir -p dist
if [[ "${1:-windows}" == "linux" ]]; then
  go build -trimpath -ldflags "$LDFLAGS" -o dist/shocking_vrchat .
  echo ">> dist/shocking_vrchat"
  exit 0
fi

echo ">> windows resources (icon, manifest)"
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64 --out rsrc

OUT=dist/shocking_vrchat_go_windows_x64
rm -rf "$OUT" && mkdir -p "$OUT"
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 \
  go build -trimpath -ldflags "$LDFLAGS -H windowsgui" -o "$OUT/shocking_vrchat.exe" .
cp -r wave_presets dg-lab "$OUT/"
cp README.md LICENSE "$OUT/"
(cd dist && rm -f shocking_vrchat_go_windows_x64.zip && \
  if command -v zip >/dev/null; then zip -qr shocking_vrchat_go_windows_x64.zip shocking_vrchat_go_windows_x64; \
  else python3 -m zipfile -c shocking_vrchat_go_windows_x64.zip shocking_vrchat_go_windows_x64; fi && \
  sha256sum shocking_vrchat_go_windows_x64.zip > sha256sums.txt)
echo ">> $OUT/shocking_vrchat.exe"
ls -la dist
