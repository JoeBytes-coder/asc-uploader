# asc-uploader 一键构建脚本（Windows）
#
# 依赖：Go 1.24+；首次构建或前端依赖变更时另需 Node.js 18+ 与 pnpm（corepack 自带）
#
# 用法：
#   powershell -ExecutionPolicy Bypass -File build.ps1                # 完整构建（前端 + CLI + mock + GUI）
#   powershell -ExecutionPolicy Bypass -File build.ps1 -SkipFrontend  # 前端已构建，只编译 Go 部分
#   powershell -ExecutionPolicy Bypass -File build.ps1 -SkipGui       # 不编译 Wails GUI（无需 WebView2 工具链）
param([switch]$SkipFrontend, [switch]$SkipGui)
$ErrorActionPreference = "Stop"
Set-Location $PSScriptRoot

# 构建环境全部落在项目目录内（不污染系统、无需管理员权限）
$env:GOPROXY    = "https://goproxy.cn,direct"
$env:GOCACHE    = Join-Path $PSScriptRoot ".build/gocache"
$env:GOMODCACHE = Join-Path $PSScriptRoot ".build/gomodcache"
$env:GOPATH     = Join-Path $PSScriptRoot ".build/gopath"
$env:TMP        = Join-Path $PSScriptRoot ".build/tmp"; $env:TEMP = $env:TMP
New-Item -ItemType Directory -Force $env:TMP, "bin" | Out-Null

# 1) 前端（Vite + Element Plus → internal/webui/dist，由 go:embed 内嵌）
if (-not $SkipFrontend) {
  Push-Location frontend
  if (-not (Test-Path "node_modules")) {
    Write-Host "[1/4] 安装前端依赖（npmmirror 镜像）..."
    $env:COREPACK_NPM_REGISTRY = "https://registry.npmmirror.com"
    $env:npm_config_registry   = "https://registry.npmmirror.com"
    corepack pnpm install
  } else {
    Write-Host "[1/4] 前端依赖已就绪"
  }
  Write-Host "[2/4] 构建前端（Vite → internal/webui/dist）..."
  corepack pnpm run build
  if ($LASTEXITCODE -ne 0) { Pop-Location; exit 1 }
  Pop-Location
} else {
  Write-Host "[1-2/4] 跳过前端构建"
}

# 2) 测试
Write-Host "[3/4] 运行 Go 测试（含端到端 mock 全流程）..."
go test ./...
if ($LASTEXITCODE -ne 0) { exit 1 }

# 3) CLI 与 mock
Write-Host "[4/4] 编译 CLI 与 mock ..."
go build -trimpath -ldflags "-s -w" -o bin/asc-uploader-cli.exe ./cmd/asc-uploader-cli
if ($LASTEXITCODE -ne 0) { exit 1 }
go build -trimpath -ldflags "-s -w" -o bin/asc-mock.exe ./cmd/asc-mock
if ($LASTEXITCODE -ne 0) { exit 1 }

# 4) Wails GUI（原生窗口版）
if (-not $SkipGui) {
  if (-not (Test-Path ".tools/wails.exe")) {
    Write-Host "     安装 Wails CLI（首次）..."
    New-Item -ItemType Directory -Force ".tools" | Out-Null
    $env:GOBIN = Join-Path $PSScriptRoot ".tools"
    go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0
  }
  & (Join-Path $PSScriptRoot ".tools/wails.exe") build -s
  if ($LASTEXITCODE -ne 0) { exit 1 }
  Write-Host "构建完成:"
  Write-Host "  GUI（原生窗口）: $PSScriptRoot\build\bin\asc-uploader-ui.exe"
} else {
  Write-Host "构建完成（跳过 GUI）:"
}
Write-Host "  CLI:             $PSScriptRoot\bin\asc-uploader-cli.exe"
Write-Host "  Mock 服务器:     $PSScriptRoot\bin\asc-mock.exe"
