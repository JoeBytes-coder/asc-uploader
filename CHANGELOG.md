# 更新日志

本项目遵循 [Keep a Changelog](https://keepachangelog.com/zh-CN/1.1.0/) 格式。

## [4.1.3] - 2025-10-19

### 文档与发布

- 新增 [docs/DEPLOY.md](docs/DEPLOY.md)：可照做的完整打包部署指南（一键构建、产物自检、Inno Setup 安装包、代码签名、GitHub 仓库创建/推送/Release、版本发布流程、常见问题）
- 新增 GitHub Actions：`.github/workflows/ci.yml`（vet + race test + 前端构建）与 `.github/workflows/release.yml`（推送 v* tag 自动构建三件产物并创建 Release）
- README 增加部署文档入口；`.gitignore` 补充 `build/bin/`、`installer/dist/`

## [4.1.2] - 2025-10-19

官方文档全量核对后的符合性修复。

### 修复

- JWT header 补上官方要求的第三个字段 `typ:"JWT"`（此前只有 alg/kid；JWT 规范中 typ 为可选、Apple 实际兼容，但为与文档完全一致而补齐，零风险）

### 核对结论（与官方文档逐项比对）

| 项 | 官方 | 实现 |
| --- | --- | --- |
| JWT header | alg=ES256 + kid + typ=JWT | ✅（本版补齐 typ） |
| 团队 payload | iss/iat/exp/aud | ✅ |
| 个人 payload | sub=user，无 iss | ✅ |
| Token 有效期 | ≤20 分钟 | ✅ 18 分钟 |
| POST /v1/buildUploads | platform(IOS/MAC_OS/TV_OS/VISION_OS) + 两个 CFBundle 字段必填 + relationships.app | ✅ |
| POST /v1/buildUploadFiles | assetType(枚举含 ASSET)/fileName/fileSize/uti(枚举含 com.apple.ipa/pkg) | ✅ |
| PATCH 提交 | uploaded + sourceFileChecksums.file{algorithm=MD5,hash} | ✅ |
| state 枚举 | AWAITING_UPLOAD/PROCESSING/COMPLETE/FAILED | ✅ |
| DeliveryFileUploadOperation | method/url/length/offset/partNumber/requestHeaders | ✅ |

## [4.1.1] - 2025-10-19

**修正 4.1.0 对个人密钥的错误认知**：经官方文档《Generating Tokens for API Requests》核实——
"Individual keys don't use the Issuer ID key iss, but do require the Subject key sub."
个人密钥**不存在也不需要 Issuer ID**（4.1.0 误以为有独立 Issuer ID），其 JWT payload 用 `sub="user"` 代替 `iss`。4.1.0 按团队 payload 签发会导致真实个人密钥被 401 拒绝，本版修复。

### 修复/变更

- `internal/jwt`：`NewIndividual`/`NewWithType`——个人密钥签发 `{aud,exp,iat,sub:"user"}` payload（无 iss）；未知类型报错
- `ascapi.NewClient`：个人密钥忽略 IssuerID 且不再要求；团队密钥缺 IssuerID 明确报错；401 提示按两种密钥分别说明
- `uploader`/`webui`/CLI/GUI 全链路：个人密钥模式**不需要填写 Issuer ID**（GUI 隐藏该输入并显示说明；CLI `--issuer-id` 仅团队密钥必填）
- `mockasc`：验签自适应——团队 token 校验 iss 匹配；个人 token 要求 sub="user" 且无 iss
- 测试：jwt 个人密钥 payload 结构断言（sub=user/无 iss/签名可验）；e2e 个人密钥全流程（免 IssuerID 走到上传完成）；团队密钥错误 IssuerID → 401；webui 个人密钥免 IssuerID 接口测试

## [4.1.0] - 2025-10-19

新增：**个人密钥（Individual Key）支持**——GUI 与 CLI 均可显式选择密钥类型。

### 新增

- GUI：卡片 1 顶部新增「密钥类型」单选（团队密钥 / 个人密钥），动态切换提示与 Issuer ID 占位符（个人密钥强调使用其独立 Issuer ID，避免误填团队 Issuer ID）；选择随 localStorage 记忆
- CLI：`--key-type team|individual`（默认 team），支持环境变量 `ASC_KEY_TYPE` 与配置项 `keyType`；上传时打印密钥类型
- `uploader.Config.KeyType` 全链路透传（/api/start → 任务日志）；`NormalizeKeyType`/`KeyTypeLabel` 带单元测试
- 401 排查提示覆盖两种密钥的 Issuer ID 来源（团队=Team Keys 页顶部；个人=该密钥自己的）

### 说明

（⚠️ 本条对个人密钥 Issuer ID 的描述有误，已在 4.1.1 依据官方文档修正：个人密钥不用 Issuer ID，payload 用 sub="user"。）个人密钥权限＝创建者本人角色，`buildUploads` 上传不在个人密钥的受限端点列表（Provisioning / Sales and Finance / notaryTool）内。

## [4.0.1] - 2025-10-19

代码质量检查修复轮（编译/vet/测试/-race/staticcheck 全部通过）。

### 修复

- **【高】`POST /api/apps` 私钥路径无效时 panic**：`mustClient` 失败已写 400 却返回 nil，被 `ListApps` 解引用崩溃；改为 `(*Client, bool)` 并立即返回，补回归测试
- **【中】进度回调数据竞争**：`speedEMA` 会被 4 个上传 worker 并发调用，加互斥锁；CLI `progressBar` 同样加锁（检查未列出的同类问题）；e2e 增加并发进度钩子，`-race` 下验证
- **【中】`Job.Finished` 跨锁读写**：新增 `Job.IsFinished()`（内部加锁），`JobManager.Start/Cancel` 改用之
- **【中】本地 API 安全校验**：`Access-Control-Allow-Origin: *` 收紧为 **Host 回环 + Origin 白名单**（127.0.0.1/localhost/::1/wails.localhost），防恶意网页 CSRF 与 DNS rebinding；校验下沉到 `Server.Handler()`，原生窗口/web 兜底两种形态统一生效；补 7 组中间件测试

### 变更

- Go 工具链升至 **go1.26.9**（go.mod `toolchain` 指令，修复 govulncheck 报告的 24 个标准库漏洞；无需改动业务代码）
- staticcheck 3 条清零：错误串不以专有名词开头（ipa/plist）、`fmt.Print(fmt.Sprintf(...))` → `fmt.Printf`
- 上传 HTTP 客户端改为包级复用（连接池跨分片/重试复用）；`io.ReadAll` 错误不再忽略（按网络错误退避）
- 前端只注册实际用到的图标（全量注册会把 300+ 图标全部打入产物），JS 产物 1191KB → **1042KB**；`chunkSizeWarningLimit` 调整为贴近实际体积的 1200（不再掩盖告警）；`index.html` 为 `api-config.js` 补充运行时注入说明

### 测试

- 新增 `internal/webui/handlers_test.go`：apps 无效私钥（回归）/ 缺参 / mock 正常链路 / parse-path / start 校验 / job 空快照 / 安全中间件 7 场景 / hostname 解析

## [4.0.0] - 2025-10-19

项目化重构：把多轮迭代的原型（Node CLI → Go+Vue 浏览器版 → Wails 原生窗口版）抽离为结构规范的完整 Golang 项目。

### 新增

- **纯 Go CLI**（cmd/asc-uploader-cli）：upload / apps / status / list / jwt 五个子命令，配置文件 + 环境变量 + 命令行三级凭据解析，TTY 进度条（速度 + ETA），退出码语义（0/1/2）——取代 v1.x 的 Node.js asc-upload.mjs
- **Mock 服务器命令行**（cmd/asc-mock）：--generate-keys 一键生成测试密钥与演示 IPA，离线演示/联调
- **Go 测试体系**：plist 往返（内置 bplist 编码器）、JWT 验签、API 客户端重试策略、IPA decoy 选取、端到端全流程（完整流水线 / no-wait / 401 链路 / webui 接口链路）
- **完整文档**：README、设计文档（docs/DESIGN.md）、构建指南（docs/BUILD.md）、Inno Setup 安装脚本
- internal/version 统一版本号；.gitignore / LICENSE(MIT) / 配置示例

### 重构

- 分层包结构：internal/{jwt,plist,ipa,ascapi,uploader,webui,mockasc,testutil}
- CLI 与 GUI 共用同一条上传流水线 uploader.Run（Hooks 进度回调）
- JobManager 显式快照类型（JobSnapshot），避免锁复制；ascapi 的 token 源抽象为接口（可测试注入）
- 前端产物内嵌位置规范化：frontend/ → internal/webui/dist（go:embed）
- 构建缓存与工具全部项目内化（.build/、.tools/），普通权限可完整重建

### 继承（自 v3.x）

- Wails v2 原生窗口（WebView2）+ --web 浏览器兜底；启动四层加固（运行时预检 / 数据目录多候选写探测 / 失败必弹窗 / 一键降级）
- 原生模式 /api 走真实回环 HTTP 服务（规避 WebView2 请求拦截对大文件 multipart 的不可靠）
- Element Plus 界面（三卡片 / 六阶段步骤条 / 实时速度 / 成功卡片）

## 历史版本（原型阶段，不在本仓库）

- v3.1（2025-05）：修复普通权限「无响应」——数据目录多候选写探测 + 失败弹窗 + --web 降级
- v3.0（2025-05）：Wails v2 原生窗口版，替换浏览器方案
- v2.x（2025-04）：Vite + Element Plus 前端工程化；Go 后端 + 本地 HTTP
- v1.x（2025-04）：Node.js 单文件 CLI（asc-upload.mjs，零依赖，25/25 测试通过）
