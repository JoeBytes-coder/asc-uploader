# 构建与发布指南

## 环境要求

| 工具 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.24+ | 全部后端与 CLI（本机验证：1.26.1） |
| Node.js | 18+ | 前端构建（本机验证：22.x） |
| pnpm | 10.x | 由 corepack 提供（`corepack enable` 或直接 `corepack pnpm ...`） |
| Wails CLI | v2.17.0 | 仅 GUI 打包（build.ps1 会自动安装到 .tools/） |
| Inno Setup 6 | 可选 | 打传统安装包 setup.exe |

> 全部构建缓存（GOCACHE/GOMODCACHE/GOPATH）与工具都落在项目 `.build/`、`.tools/` 目录内，不污染系统、不需要管理员权限。国内网络默认走 goproxy.cn 与 npmmirror 镜像。

## 一键构建

```powershell
powershell -ExecutionPolicy Bypass -File build.ps1                # 完整：前端 → 测试 → CLI → mock → GUI
powershell -ExecutionPolicy Bypass -File build.ps1 -SkipFrontend  # 前端已构建，只重编 Go
powershell -ExecutionPolicy Bypass -File build.ps1 -SkipGui       # 不编译 Wails GUI
```

产物：

| 文件 | 说明 |
| --- | --- |
| build/bin/asc-uploader-ui.exe | Wails 原生窗口 GUI（约 13MB，双击即用） |
| bin/asc-uploader-cli.exe | 纯 Go CLI（约 7MB，跨平台可重编译） |
| bin/asc-mock.exe | Mock App Store Connect 服务器 |

## 分步构建

### 1. 前端（Vite + Element Plus → internal/webui/dist）

```powershell
cd frontend
$env:COREPACK_NPM_REGISTRY = "https://registry.npmmirror.com"
corepack pnpm install      # 首次或依赖变更
corepack pnpm run build    # 产物输出到 ../internal/webui/dist
```

> 仓库中 `internal/webui/dist/index.html` 是占位文件，保证未构建前端时 `go build ./...` 仍可通过；真实构建后被完整产物替换。

### 2. Go 侧

```powershell
go test ./...                                        # 测试（含离线端到端）
go build -trimpath -ldflags "-s -w" -o bin/asc-uploader-cli.exe ./cmd/asc-uploader-cli
go build -trimpath -ldflags "-s -w" -o bin/asc-mock.exe ./cmd/asc-mock
.tools/wails.exe build -s                            # GUI（-s = 跳过前端构建步骤）
```

### 3. 安装包（可选）

```powershell
# 传统 setup.exe（Inno Setup 6）
iscc installer\asc-uploader-ui.iss    # 产物：installer/dist/asc-uploader-ui-setup.exe
```

需要标准 `.msi` 格式时，把 iss 替换为 WiX 的 `.wxs` 工程即可（入口唯一：build/bin/asc-uploader-ui.exe）。

## 前端开发（热更新）

```powershell
# 终端 1：后端（web 模式固定端口，供 vite 代理）
build\bin\asc-uploader-ui.exe --web --port 17841 --no-open

# 终端 2：vite dev（/api 已代理到 127.0.0.1:17841）
cd frontend && corepack pnpm dev
```

GUI 联调小技巧：临时需要 DevTools 时，启动前设置环境变量
`WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222`（生产默认关闭）。

## 发布清单

1. `build.ps1` 全量构建 + `go test ./...` 通过
2. 用 `bin/asc-mock.exe` 离线冒烟一遍 upload 全流程（见 README「离线演示」）
3. 更新 CHANGELOG.md 与 internal/version/version.go
4. 打 tag（如 v4.0.0）并附上三个 exe

## 常见构建问题

| 现象 | 处理 |
| --- | --- |
| `go:embed dist` 报错 | internal/webui/dist 缺失：先构建前端，或确认占位 index.html 存在 |
| vite 找不到 esbuild | 重新 `corepack pnpm install`（pnpm 符号链接损坏时删除 node_modules 重装） |
| wails build 卡在下载 | 确认 GOPROXY=goproxy.cn；或手动 `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0` |
| 前端改了但界面没变 | 确认 vite 输出到了 internal/webui/dist 并重新编译了 GUI（go:embed 在编译期打包） |
