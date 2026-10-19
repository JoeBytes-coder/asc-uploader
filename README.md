# asc-uploader

> Windows 可用的 iOS 安装包（.ipa / .pkg）上传工具 —— 基于 Apple 官方 **App Store Connect API** 的 `buildUploads` 接口（2025 年新增），**无需 Xcode、无需 Transporter、无需 macOS**，即可把构建上传到 App Store Connect / TestFlight。

[![Go](https://img.shields.io/badge/Go-1.25+-00ADD8)]() [![Wails](https://img.shields.io/badge/Wails-v2.17-blue)]() [![Vue](https://img.shields.io/badge/Vue-3-42b883)]() [![Element Plus](https://img.shields.io/badge/Element%20Plus-2.x-409EFF)]()

本仓库是完整的 **Golang 项目**：纯 Go 命令行工具 + Go(Wails) 原生窗口 GUI + Go 测试设施（内置 Mock 服务器），全部业务逻辑零第三方业务依赖（GUI 壳依赖 Wails v2）。

---

## 目录

- [功能特性](#功能特性)
- [项目结构](#项目结构)
- [快速开始](#快速开始)
  - [GUI 版（推荐日常使用）](#gui-版推荐日常使用)
  - [CLI 版（推荐 CI/CD）](#cli-版推荐-cicd)
  - [离线演示（Mock 服务器）](#离线演示mock-服务器)
- [获取 API 密钥](#获取-api-密钥)
- [构建](#构建)
- [测试](#测试)
- [常见问题排查](#常见问题排查)
- [设计文档](docs/DESIGN.md)

## 功能特性

- **官方五步上传流程全实现**：创建会话 → 预留文件位 → 并发分片上传 → MD5 提交 → 轮询处理状态（`AWAITING_UPLOAD → PROCESSING → COMPLETE/FAILED`）
- **版本号 / 构建号自动读取**：内置 zip + 二进制 plist（bplist00）/ XML plist 解析器，直接从 .ipa 读出 `CFBundleShortVersionString` / `CFBundleVersion`，无需手动填写
- **ES256 JWT 自动签发**：App Store Connect API 密钥（.p8）现场签发 Token（18 分钟有效、Apple 上限 20 分钟），过期自动刷新、401 自动重签
- **并发分片上传引擎**：4 并发、失败自动指数退避重试、实时进度（CLI 进度条 / GUI 步骤条 + 速度 + ETA）
- **中文错误排查提示**：401/403/404/429、重复构建号等常见错误自动附带排查建议
- **双形态交付**：
  - `asc-uploader-ui.exe` —— Wails v2 原生窗口（WebView2 内嵌，不启动外部浏览器，普通用户权限即可运行）
  - `asc-uploader-cli.exe` —— 纯 Go 静态 CLI（Windows / Linux / macOS 通用，适合 CI）
- **完整离线测试设施**：Go 实现的 Mock App Store Connect 服务器（JWT 验签、分片字节校验、MD5 比对、状态机轮转），端到端测试不联网、不消耗 API 配额

## 项目结构

```
asc-uploader/
├── main.go                     # GUI 入口（Wails v2 原生窗口 + --web 兜底模式）
├── wails.json                  # Wails 构建配置
├── build/                      # Wails 构建资源（应用图标 / manifest）
├── cmd/
│   ├── asc-uploader-cli/       # 纯 Go CLI（upload/apps/status/list/jwt）
│   └── asc-mock/               # Mock App Store Connect 服务器（离线演示/联调）
├── internal/
│   ├── jwt/                    # ES256 JWT 签发与缓存
│   ├── plist/                  # 二进制/XML plist 解析器
│   ├── ipa/                    # .ipa 安装包信息读取（zip → Info.plist）
│   ├── ascapi/                 # App Store Connect API 客户端（重试/分片上传）
│   ├── uploader/               # 六阶段上传编排 + 后台任务管理
│   ├── webui/                  # GUI 本地服务（内嵌前端 + /api 接口）
│   ├── mockasc/                # Mock 服务器实现（测试/演示）
│   ├── testutil/               # 测试夹具（bplist 编码器/IPA 构造器/密钥生成）
│   └── version/                # 统一版本号
├── frontend/                   # Vue 3 + Element Plus（Vite 构建）
│   └── src/App.vue             # 单页界面：认证 → 选包 → 上传
├── test/e2e/                   # 端到端测试（真实走完上传全流程）
├── configs/                    # 配置示例
├── installer/                  # Inno Setup 安装包脚本（可选）
├── docs/                       # 文档（DESIGN.md / BUILD.md / 截图）
└── build.ps1                   # 一键构建脚本
```

## 快速开始

### GUI 版（推荐日常使用）

1. 运行 `build\bin\asc-uploader-ui.exe`（双击，无需管理员权限）
2. 第 1 张卡片：选择 .p8 私钥文件、填 Key ID 与 Issuer ID → 点「测试连接 / 获取 App 列表」
3. 第 2 张卡片：把 .ipa 拖进拖拽区（或直接填本机路径），版本号/构建号自动读出
4. 第 3 张卡片：点「上传并等待处理结果」——六阶段步骤条、分片进度、实时速度、最终 Build ID

界面截图见 [docs/ui-final.png](docs/ui-final.png)。

> 极少数无 WebView2 运行时的旧机器：启动时会弹窗提示，一键降级 `--web` 浏览器兼容模式（功能完全相同）。

### CLI 版（推荐 CI/CD）

```bat
:: ① 列出 App（查询数字 App ID）
asc-uploader-cli.exe apps --key-file AuthKey_XXX.p8 --key-id XXX --issuer-id 你的UUID

:: ② 上传并等待 Apple 处理完成
asc-uploader-cli.exe.exe upload --key-file AuthKey_XXX.p8 --key-id XXX ^
    --issuer-id 你的UUID --app-id 987654321 --ipa MyApp.ipa

:: ③ 或使用配置文件 / 环境变量（ASC_KEY_FILE / ASC_KEY_ID / ASC_ISSUER_ID / ASC_APP_ID）
asc-uploader-cli.exe upload --config configs\asc-config.example.json --ipa MyApp.ipa
```

子命令：`upload`（上传）、`apps`（列 App）、`status --id <会话ID>`（查状态）、`list --app-id <id>`（历史记录）、`jwt`（调试认证）。个人密钥上传加 `--key-type individual`——此时不需要 `--issuer-id`（个人密钥认证用 `sub=user`，无 Issuer ID）。

退出码：`0` 成功；`1` 失败；`2` 字节已全部上传但等待处理超时（Apple 侧处理慢，属正常）。

### 离线演示（Mock 服务器）

```bat
:: 生成测试密钥与演示安装包（一次性）
bin\asc-mock.exe --generate-keys .build\smoke
:: 启动模拟服务器（128 字节小分片，便于观察并发上传）
bin\asc-mock.exe --pubkey .build\smoke\pubkey.pem --part 128
:: 另开一个终端，对着 mock 完整走一遍上传
bin\asc-uploader-cli.exe upload --key-file .build\smoke\AuthKey_MOCKKEY001.p8 ^
    --key-id MOCKKEY001 --issuer-id mock-issuer --app-id 123456789 ^
    --ipa .build\smoke\Demo.ipa --api-base http://127.0.0.1:<mock打印的端口>
```

## 获取 API 密钥

三样凭据都来自 [App Store Connect](https://appstoreconnect.apple.com) 网页端（需**付费 Apple Developer Program** + **账户持有人/管理员**身份）：

1. 登录后进入 **用户和访问 → 集成 → App Store Connect API**
2. 点 **添加 API 密钥**：名称随意，角色选 **App Manager（应用管理员）**——能上传构建的最低常用角色
3. 创建完成后：

| 凭据 | 位置 | 形态 |
| --- | --- | --- |
| **Issuer ID** | 密钥页面顶部说明文字（整个团队共用） | UUID |
| **Key ID** | 密钥列表的表格行 | 约 10 位 |
| **.p8 私钥** | 该行「下载 API 密钥」链接 | `AuthKey_XXX.p8` |

⚠️ **.p8 只能下载一次**，立即妥善保存；不要提交进 Git 或发给他人。私钥只在本地用于签发 JWT，不会发送到 Apple 以外的地方。

**个人密钥（Individual Key）也可以用来上传**：在任意 App Store Connect 页面右上角点你的用户名 → 编辑个人资料 → Individual API Key → Generate API Key。个人密钥同样会生成 .p8 文件（同样只能下载一次），权限等同于创建者本人的角色（不能访问 Provisioning / Sales and Finance / notaryTool，但 **buildUploads 上传不在受限列表**）。**按官方文档，个人密钥不使用 Issuer ID**——JWT 认证 payload 用 `sub="user"` 代替 `iss`，因此只需 .p8 + Key ID 两样凭据（工具里选择「个人密钥」类型后无需填 Issuer ID）；每个用户同一时间只能有一把活跃的个人密钥。前提不变：必须是付费 Apple Developer Program，且账户持有人已在「集成」页开通 App Store Connect API。

## 构建

```powershell
# 完整构建（前端 + 测试 + CLI + mock + Wails GUI）
powershell -ExecutionPolicy Bypass -File build.ps1

# 只改了 Go 代码时
powershell -ExecutionPolicy Bypass -File build.ps1 -SkipFrontend
```

依赖：Go 1.24+；首次构建前端另需 Node.js 18+（pnpm 由 corepack 提供，自动走 npmmirror 镜像）。详细说明见 [docs/BUILD.md](docs/BUILD.md)；**打包、安装包制作与 GitHub 发布**见 [docs/DEPLOY.md](docs/DEPLOY.md)（含一键命令与 CI 工作流说明）。

## 测试

```powershell
go test ./...
```

覆盖：plist 二进制/XML 解析（编码器往返）、JWT 签发与验签、API 客户端重试策略（401 重签 / 5xx 退避 / 4xx 不重试）、.ipa 最短路径选取、以及**端到端全流程**（IPA 解析 → JWT 验签 → 8 分片并发上传 → MD5 提交 → PROCESSING → COMPLETE → Build ID）。

## 常见问题排查

| 现象 | 原因与处理 |
| --- | --- |
| 401 NOT_AUTHORIZED | Key ID 与 .p8 不匹配 / Issuer ID 填错 / 系统时间不准。工具会自动重签一次再失败才报错 |
| 403 | 密钥角色不足：至少 App Manager，且该 App 在密钥可访问范围内 |
| 404 | App ID 不对：用 `apps` 命令查真实数字 ID |
| 重复构建号（already/duplicate） | 该版本号+构建号已存在：递增 `CFBundleVersion` 重新打包 |
| 429 / 5xx | 限流或服务端抖动：自动指数退避重试（最多 4 次） |
| GUI 双击「无响应」 | v4.0 已加固：数据目录多候选写探测 + 失败必弹窗 + 一键降级 --web；仍异常时把弹窗内容反馈给开发者 |
| 远程桌面里窗口黑屏 | GPU 加速问题：设环境变量 `WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--disable-gpu` 或用 `--web` |
| 未签名 exe 被 SmartScreen 拦 | 「更多信息 → 仍要运行」；介意的话用 installer/ 下的 Inno Setup 脚本自行打包签名 |

## 许可证

[MIT](LICENSE)
