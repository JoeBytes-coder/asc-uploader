# 打包与部署指南

> 目标：照本文档操作，可在一台 Windows 机器上完成**完整打包**（GUI + CLI + 安装包），并把代码与产物**发布到 GitHub**。

## 1. 环境要求

| 工具 | 版本 | 用途 |
| --- | --- | --- |
| Go | 1.24+（go.mod 指定 toolchain go1.26.9，会自动下载） | 后端 / CLI / GUI |
| Node.js | 18+ | 前端构建 |
| pnpm | 10.x（corepack 提供） | 依赖安装 |
| Wails CLI | v2.17.0（build.ps1 自动安装到 .tools/） | GUI 打包 |
| Inno Setup 6 | 可选 | 生成 setup.exe 安装包 |
| Git | 任意较新版本 | 版本管理 / 发布 |

> 全部构建缓存落在项目内 `.build/`、`.tools/`，不污染系统；国内网络默认走 goproxy.cn / npmmirror 镜像。

## 2. 一键打包

```powershell
cd D:\Code\asc-uploader
powershell -ExecutionPolicy Bypass -File build.ps1
```

流程：前端构建（Vite → internal/webui/dist）→ `go test ./...` → CLI / mock 编译 → Wails GUI 编译。

产物：

| 文件 | 说明 |
| --- | --- |
| `build/bin/asc-uploader-ui.exe` | Wails 原生窗口 GUI（约 13MB，双击即用） |
| `bin/asc-uploader-cli.exe` | 纯 Go CLI（约 7MB） |
| `bin/asc-mock.exe` | Mock 服务器（演示 / 联调 / 冒烟） |

可选参数：`-SkipFrontend`（前端未变时跳过构建）、`-SkipGui`（不打包 GUI）。

## 3. 产物自检（发布前必做）

```powershell
# ① 版本一致性
bin\asc-uploader-cli.exe version
# ② 离线冒烟：mock 全流程（个人密钥 + 团队密钥各一次）
bin\asc-mock.exe --generate-keys .build\smoke
bin\asc-mock.exe --pubkey .build\smoke\pubkey.pem --part 128   # 记下打印的端口
# 另开终端：
bin\asc-uploader-cli.exe upload --key-type individual --key-file .build\smoke\AuthKey_MOCKKEY001.p8 --key-id MOCKKEY001 --app-id 123456789 --ipa .build\smoke\Demo.ipa --api-base http://127.0.0.1:<端口>
# ③ GUI：双击 build\bin\asc-uploader-ui.exe，或 --web 模式验证
build\bin\asc-uploader-ui.exe --web --port 17899 --no-open
```

## 4. 制作安装包（可选）

**Inno Setup（setup.exe）**：

```powershell
# 安装 Inno Setup 6 后：
iscc installer\asc-uploader-ui.iss
# 产物：installer/dist/asc-uploader-ui-setup.exe
```

脚本要点：`PrivilegesRequired=lowest`（普通权限安装到用户目录）、带桌面快捷方式与卸载器、版本号与 `internal/version/version.go` 保持一致。

**需要 .msi 格式**：用 WiX Toolset 重写 `.wxs`（入口唯一：`build/bin/asc-uploader-ui.exe`），命令 `wix build -o dist\asc-uploader-ui.msi installer\asc-uploader-ui.wxs`。

## 5. 代码签名（可选，企业分发建议）

```powershell
# 有代码签名证书时（SignTool 来自 Windows SDK）：
signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /a build\bin\asc-uploader-ui.exe
```

未签名 exe 首次运行会被 SmartScreen 提示（更多信息 → 仍要运行），属正常现象。

## 6. 发布到 GitHub

### 6.1 初次初始化（本仓库已执行的步骤）

```bat
cd D:\Code\asc-uploader
git init
git config user.name  "你的GitHub用户名"
git config user.email "你的GitHub邮箱或 <用户名>@users.noreply.github.com"
git add -A
git commit -m "feat: asc-uploader v4.1.3"
```

> `.gitignore` 已排除：`.build/`、`bin/`、`build/bin/`、`.tools/`、`frontend/node_modules/`、`internal/webui/dist/assets/`、`*.p8`（私钥绝不入库）、`testdata/`。前端产物在构建时由 `go:embed` 打包，不入库。

### 6.2 创建远程仓库

**方式 A：GitHub 网页**（推荐首次）——https://github.com/new → Repository name 填 `asc-uploader` → 选择 Private/Public → **不要勾选** Initialize with README → Create。

**方式 B：gh CLI**：

```powershell
winget install GitHub.cli
gh auth login
gh repo create asc-uploader --private --source . --remote origin --push
```

**方式 C：API（有 PAT 时）**：

```powershell
$h = @{ Authorization = "Bearer <PAT>"; Accept = "application/vnd.github+json" }
Invoke-RestMethod -Method Post -Uri https://api.github.com/user/repos -Headers $h -Body (@{ name = 'asc-uploader'; private = $true } | ConvertTo-Json)
```

### 6.3 配置远程并推送

```powershell
# SSH（本机已配置，验证：ssh -T git@github.com）
git remote add origin git@github.com:<你的用户名>/asc-uploader.git
# 或 HTTPS（Git Credential Manager 会记住 PAT）
# git remote add origin https://github.com/<你的用户名>/asc-uploader.git

git branch -M main
git push -u origin main
```

### 6.4 打 tag 并发布 Release（附可执行文件）

```powershell
git tag -a v4.1.3 -m "asc-uploader v4.1.3"
git push origin v4.1.3
```

然后：GitHub 仓库 → Releases → **Draft a new release** → 选择 tag `v4.1.3` → 上传 `asc-uploader-ui.exe`、`asc-uploader-cli.exe`、`asc-mock.exe`（及可选的 setup.exe）→ Publish。

命令行方式（gh）：

```powershell
gh release create v4.1.3 build/bin/asc-uploader-ui.exe bin/asc-uploader-cli.exe bin/asc-mock.exe --generate-notes
```

> 打 tag 后若已推送，`.github/workflows/release.yml` 会在 GitHub Actions 自动构建并创建 Release（见第 8 节）——可跳过手动上传。

### 6.5 发布校验

- Release 页面三个文件可下载，大小与本地一致（GUI ≈13MB / CLI ≈7MB / mock ≈6MB）
- 下载后 `asc-uploader-cli.exe version` 输出与 tag 一致
- GUI 双击可正常打开（或 `--web` 验证）

## 7. 本地 CI 预跑（推送前）

```powershell
go vet ./...
go test -race ./...
# 前端构建无告警
cd frontend; corepack pnpm run build
```

## 8. GitHub Actions

仓库内置两条工作流（`.github/workflows/`）：

| 工作流 | 触发 | 行为 |
| --- | --- | --- |
| `ci.yml` | push / PR | Go vet + test（含 -race）、前端构建、CLI 构建 |
| `release.yml` | 推送 `v*` tag | Windows 上完整构建（前端 + GUI + CLI + mock）并自动创建 GitHub Release |

首次运行前确认：仓库 Settings → Actions → General → Workflow permissions 允许 **Read and write**（release.yml 需要写 Release）。

## 9. 版本发布流程（推荐）

1. 修改 `internal/version/version.go` 的 `Version`（如 `4.1.4`）
2. 更新 `CHANGELOG.md`
3. `build.ps1` 完整构建 + 第 3 节自检
4. `git add -A && git commit -m "release: v4.1.4" && git push`
5. `git tag -a v4.1.4 -m "v4.1.4" && git push origin v4.1.4`（CI 自动出 Release）

## 10. 常见问题

| 现象 | 处理 |
| --- | --- |
| `go:embed dist` 失败 | 先构建前端（`build.ps1` 或 `cd frontend && pnpm run build`） |
| Go 依赖下载慢/失败 | 确认 `GOPROXY=https://goproxy.cn,direct`（build.ps1 已内置） |
| pnpm 安装慢 | 使用 npmmirror（build.ps1 已内置）或 `corepack pnpm install --registry=https://registry.npmmirror.com` |
| Wails 下载失败 | 手动 `go install github.com/wailsapp/wails/v2/cmd/wails@v2.17.0` 并把 `%GOPATH%\bin` 加入 PATH |
| 推送被拒（Authentication failed） | 用 SSH：`ssh -T git@github.com` 验证；或 `gh auth login` 配置 HTTPS token |
| Actions 无权限创建 Release | 仓库 Settings → Actions → Workflow permissions → Read and write |
| 杀毒软件拦截 exe | 添加信任或进行代码签名（第 5 节） |
