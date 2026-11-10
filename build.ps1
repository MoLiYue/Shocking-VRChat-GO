# Build on Windows (PowerShell). Requires Go >= 1.22 and Node.js >= 18.
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot
$Version = if ($env:VERSION) { $env:VERSION } else { "0.7.19" }
$ldflags = "-s -w -H windowsgui -X shockingvrc/internal/app.Version=$Version"

if ($env:SKIP_FRONTEND -ne "1") {
  Push-Location frontend
  if (-not (Test-Path node_modules)) { npm ci --no-audit --no-fund }
  npm run build
  Pop-Location
}
go run ./tools/packassets
go test ./...
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres/winres.json --arch amd64 --out rsrc

$out = "dist\shocking_vrchat_go_windows_x64"
if (Test-Path $out) { Remove-Item -Recurse -Force $out }
New-Item -ItemType Directory -Force $out | Out-Null
$env:CGO_ENABLED = "0"
go build -trimpath -ldflags $ldflags -o "$out\shocking_vrchat.exe" .
Copy-Item -Recurse wave_presets, dg-lab "$out\"
Copy-Item README.md, LICENSE "$out\"
Compress-Archive -Force -Path $out -DestinationPath "dist\shocking_vrchat_go_windows_x64.zip"
Write-Host "Built $out\shocking_vrchat.exe"
