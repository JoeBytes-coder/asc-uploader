// asc-uploader-cli 是纯 Go 的命令行版构建上传工具（Windows/Linux/macOS 通用）。
//
// 完整实现 App Store Connect API 的官方上传流程，无需 Xcode / Transporter / macOS。
// 退出码：0 成功；1 失败；2 字节已上传完成但等待处理超时。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"asc-uploader/internal/ascapi"
	"asc-uploader/internal/ipa"
	"asc-uploader/internal/jwt"
	"asc-uploader/internal/uploader"
	"asc-uploader/internal/version"
)

/* ---------------- 输出工具（ANSI 着色，非 TTY 自动关闭） ---------------- */

var useColor = func() bool {
	fi, err := os.Stdout.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && os.Getenv("NO_COLOR") == ""
}()

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func green(s string) string  { return paint("32", s) }
func red(s string) string    { return paint("31", s) }
func yellow(s string) string { return paint("33", s) }
func dim(s string) string    { return paint("2", s) }
func bold(s string) string   { return paint("1", s) }
func cyan(s string) string   { return paint("36", s) }

var quiet = false

func log(msg string) {
	if !quiet {
		fmt.Println(msg)
	}
}

func info(msg string) { log(" " + cyan("i") + " " + msg) }
func ok(msg string)   { fmt.Println(" " + green("✓") + " " + msg) }
func warn(msg string) { fmt.Println(" " + yellow("!") + " " + msg) }

func failExit(msg string) {
	fmt.Fprintln(os.Stderr, " "+red("×")+" "+msg)
	os.Exit(1)
}

func fmtDuration(sec float64) string {
	if sec < 0 {
		sec = 0
	}
	s := int(sec + 0.5)
	return fmt.Sprintf("%02d:%02d", s/60, s%60)
}

func pad(s string, n int) string {
	for len(s) < n {
		s += " "
	}
	return s
}

/* ---------------- 凭据解析：命令行 > 环境变量 > 配置文件 ---------------- */

type cliConfig struct {
	KeyFile  string `json:"keyFile"`
	KeyID    string `json:"keyId"`
	IssuerID string `json:"issuerId"`
	AppID    string `json:"appId"`
	APIBase  string `json:"apiBase"`
	KeyType  string `json:"keyType"` // team（默认）/ individual
}

type cliFlags struct {
	fs       *flag.FlagSet
	keyFile  *string
	keyID    *string
	issuerID *string
	appID    *string
	config   *string
	apiBase  *string
	quiet    *bool
	debug    *bool
	// upload 专用
	ipaPath      *string
	ver          *string
	build        *string
	platform     *string
	noWait       *bool
	pollInterval *float64
	waitTimeout  *float64
	// 密钥类型（team / individual；两种密钥协议相同，影响提示与日志）
	keyType *string
	// status / list 专用
	id    *string
	limit *int
}

func newFlags(name string) *cliFlags {
	f := &cliFlags{fs: flag.NewFlagSet(name, flag.ExitOnError)}
	f.keyFile = f.fs.String("key-file", "", "App Store Connect API 私钥（.p8）路径")
	f.keyID = f.fs.String("key-id", "", "Key ID（AuthKey_ 文件名后半段）")
	f.issuerID = f.fs.String("issuer-id", "", "Issuer ID（密钥页顶部的 UUID）")
	f.appID = f.fs.String("app-id", "", "App Store Connect 的 App 数字 ID")
	f.config = f.fs.String("config", "", "JSON 配置文件（keyFile/keyId/issuerId/appId/apiBase/keyType）")
	f.keyType = f.fs.String("key-type", "team", "密钥类型：team（团队密钥）/ individual（个人密钥）")
	f.apiBase = f.fs.String("api-base", "", "API 地址（默认官方，测试用）")
	f.quiet = f.fs.Bool("quiet", false, "减少日志输出")
	f.debug = f.fs.Bool("debug", false, "打印调试信息")
	f.ipaPath = f.fs.String("ipa", "", "（upload）安装包路径 .ipa / .pkg")
	f.ver = f.fs.String("set-version", "", "（upload）覆盖版本号（默认读自包内）")
	f.build = f.fs.String("build", "", "（upload）覆盖构建号（默认读自包内）")
	f.platform = f.fs.String("platform", "IOS", "（upload）IOS / MAC_OS / TV_OS / VISION_OS")
	f.noWait = f.fs.Bool("no-wait", false, "（upload）上传字节后不等待 Apple 处理")
	f.pollInterval = f.fs.Float64("poll-interval", 5, "（upload）状态轮询间隔秒数")
	f.waitTimeout = f.fs.Float64("wait-timeout", 1800, "（upload）等待处理超时秒数")
	f.id = f.fs.String("id", "", "（status）buildUpload 会话 ID")
	f.limit = f.fs.Int("limit", 20, "（list）最多列出的记录数")
	return f
}

func resolveCreds(f *cliFlags) cliConfig {
	var cfg cliConfig
	if *f.config != "" {
		raw, err := os.ReadFile(*f.config)
		if err != nil {
			failExit("读取配置文件失败: " + err.Error())
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			failExit("解析配置文件失败: " + err.Error())
		}
	}
	pick := func(flagVal, envKey, cur string) string {
		if flagVal != "" {
			return flagVal
		}
		if v := os.Getenv(envKey); v != "" {
			return v
		}
		return cur
	}
	cfg.KeyFile = pick(*f.keyFile, "ASC_KEY_FILE", cfg.KeyFile)
	cfg.KeyID = pick(*f.keyID, "ASC_KEY_ID", cfg.KeyID)
	cfg.IssuerID = pick(*f.issuerID, "ASC_ISSUER_ID", cfg.IssuerID)
	cfg.APIBase = pick(*f.apiBase, "ASC_API_BASE", cfg.APIBase)
	cfg.AppID = pick(*f.appID, "ASC_APP_ID", cfg.AppID)
	cfg.KeyType = uploader.NormalizeKeyType(pick(*f.keyType, "ASC_KEY_TYPE", cfg.KeyType))
	if cfg.APIBase == "" {
		cfg.APIBase = ascapi.DefaultBase
	}
	var missing []string
	if cfg.KeyFile == "" {
		missing = append(missing, "--key-file（或环境变量 ASC_KEY_FILE / 配置项 keyFile）")
	}
	if cfg.KeyID == "" {
		missing = append(missing, "--key-id（或 ASC_KEY_ID / keyId）")
	}
	if cfg.IssuerID == "" && cfg.KeyType == uploader.KeyTypeTeam { // 个人密钥不使用 Issuer ID（JWT 用 sub=user）
		missing = append(missing, "--issuer-id（或 ASC_ISSUER_ID / issuerId；团队密钥必填，个人密钥不需要）")
	}
	if len(missing) > 0 {
		failExit("缺少 App Store Connect API 认证信息：\n     · " + strings.Join(missing, "\n     · ") +
			"\n     密钥在 App Store Connect → 用户和访问 → 集成 → App Store Connect API 中创建（角色至少 App Manager）。")
	}
	cfg.KeyFile, _ = filepath.Abs(cfg.KeyFile)
	return cfg
}

func newClientOrExit(cfg cliConfig) *ascapi.Client {
	c, err := ascapi.NewClient(ascapi.Credentials{
		KeyPath: cfg.KeyFile, KeyID: cfg.KeyID, IssuerID: cfg.IssuerID, APIBase: cfg.APIBase,
		KeyType: cfg.KeyType,
	})
	if err != nil {
		failExit(err.Error())
	}
	return c
}

func printAPIError(err error, prefix string) {
	msg := err.Error()
	if ae, ok := err.(*ascapi.Error); ok {
		msg = ae.WithHint()
	}
	fmt.Fprintln(os.Stderr, " "+red("×")+" "+prefix+"："+msg)
}

/* ---------------- upload ---------------- */

func cmdUpload(f *cliFlags) {
	quiet = *f.quiet
	cfg := resolveCreds(f)
	if *f.ipaPath == "" {
		failExit("必须指定 --ipa <文件路径>（.ipa 或 .pkg）")
	}
	ipaPath, _ := filepath.Abs(*f.ipaPath)
	if _, err := os.Stat(ipaPath); err != nil {
		failExit("找不到文件: " + ipaPath)
	}
	if cfg.AppID == "" {
		failExit("必须指定 --app-id <数字ID>（可用 apps 命令查询；也可写在配置文件 appId 中）")
	}
	if _, err := ipa.UTI(ipaPath); err != nil {
		failExit(err.Error())
	}
	platform := strings.ToUpper(*f.platform)
	switch platform {
	case "IOS", "MAC_OS", "TV_OS", "VISION_OS":
	default:
		failExit("无效的平台 " + platform + "，可选值: IOS, MAC_OS, TV_OS, VISION_OS")
	}

	fmt.Printf("%s v%s — App Store Connect 构建上传\n", bold("asc-uploader-cli"), version.Version)
	info("密钥类型：" + uploader.KeyTypeLabel(cfg.KeyType))
	bar := newProgressBar()
	hooks := uploader.Hooks{
		Log:      func(msg string) { bar.breakLine(); log("      " + dim(msg)) },
		Phase:    func(p string) { bar.breakLine(); info("【" + uploader.PhaseName(p) + "】") },
		Progress: func(done, total int64, bps float64) { bar.update(done, total, bps) },
	}
	req := uploader.Request{
		AppID: cfg.AppID, IpaPath: ipaPath, Version: *f.ver, Build: *f.build,
		Platform: platform, Wait: !*f.noWait,
		PollInterval: time.Duration(*f.pollInterval * float64(time.Second)),
		WaitTimeout:  time.Duration(*f.waitTimeout * float64(time.Second)),
	}
	res, err := uploader.Run(context.Background(), uploader.Config{
		KeyPath: cfg.KeyFile, KeyID: cfg.KeyID, IssuerID: cfg.IssuerID, APIBase: cfg.APIBase,
		KeyType: cfg.KeyType,
	}, req, hooks)
	bar.finish()
	if err != nil {
		if err == uploader.ErrCanceled {
			warn("已取消。")
			os.Exit(1)
		}
		printAPIError(err, "上传失败")
		os.Exit(1)
	}
	if res.BuildID != "" {
		ok(bold("上传完成！Apple 已接收该构建。"))
		fmt.Printf("      Build ID: %s\n      请到 App Store Connect（TestFlight / 构建版本页）查看后续处理进度。\n", res.BuildID)
		return
	}
	if *f.noWait {
		ok("字节上传完成，会话 " + res.BuildUploadID + " 正在 Apple 侧排队处理。")
		fmt.Printf("     后续运行 status --id %s 查询处理结果。\n", res.BuildUploadID)
		return
	}
	// 等待处理但超时（字节已全部上传成功）
	warn("等待超时。字节已全部上传成功，Apple 处理耗时较长属正常现象。")
	fmt.Printf("     请稍后运行: asc-uploader-cli status --id %s\n", res.BuildUploadID)
	os.Exit(2)
}

/* ---------------- apps / status / list / jwt ---------------- */

func cmdApps(f *cliFlags) {
	cfg := resolveCreds(f)
	apps, err := ascapi.ListApps(context.Background(), newClientOrExit(cfg))
	if err != nil {
		printAPIError(err, "获取 App 列表失败")
		os.Exit(1)
	}
	if len(apps) == 0 {
		warn("没有查到任何 App（请检查密钥权限与访问范围）。")
		return
	}
	fmt.Println(" " + pad(bold("App ID"), 12) + " " + pad(bold("Bundle ID"), 38) + " " + bold("名称"))
	for _, a := range apps {
		fmt.Println(" " + pad(a.ID, 12) + " " + pad(a.BundleID, 38) + " " + a.Name)
	}
	fmt.Println(dim(fmt.Sprintf("  共 %d 个 App。上传时把对应数字 ID 传给 --app-id。", len(apps))))
}

func cmdStatus(f *cliFlags) {
	cfg := resolveCreds(f)
	if *f.id == "" {
		failExit("必须指定 --id <buildUploadId>（上传时输出的会话 ID）")
	}
	doc, err := ascapi.GetBuildUpload(context.Background(), newClientOrExit(cfg), *f.id)
	if err != nil {
		printAPIError(err, "查询失败")
		os.Exit(1)
	}
	fmt.Println("  会话 ID   : " + doc.ID)
	fmt.Println("  状态      : " + bold(doc.State.State))
	fmt.Printf("  版本/构建 : %s (%s)  平台: %s\n", doc.Version, doc.Build, doc.Platform)
	for _, e := range doc.State.Errors {
		fmt.Println("      " + red("错误") + " [" + e.Code + "] " + e.Description)
	}
	for _, w := range doc.State.Warnings {
		fmt.Println("      " + yellow("警告") + " [" + w.Code + "] " + w.Description)
	}
	if doc.BuildID != "" {
		fmt.Printf("  Build     : %s（版本 %s，处理状态 %s）\n", doc.BuildID, doc.BuildVersion, doc.BuildProcessingState)
	}
	switch doc.State.State {
	case "COMPLETE":
		ok("该构建已被 Apple 成功接收。")
	case "FAILED":
		os.Exit(1)
	}
}

func cmdList(f *cliFlags) {
	cfg := resolveCreds(f)
	if cfg.AppID == "" {
		failExit("必须指定 --app-id（或配置文件中的 appId）")
	}
	rows, err := ascapi.ListBuildUploads(context.Background(), newClientOrExit(cfg), cfg.AppID, *f.limit)
	if err != nil {
		printAPIError(err, "查询失败")
		os.Exit(1)
	}
	if len(rows) == 0 {
		warn("该 App 暂无上传记录。")
		return
	}
	fmt.Println(" " + pad(bold("会话 ID"), 14) + " " + pad(bold("状态"), 16) + " " + pad(bold("版本(构建)"), 20) + " " + bold("平台"))
	for _, r := range rows {
		fmt.Println(" " + pad(r.ID, 14) + " " + pad(r.State.State, 16) + " " + pad(r.Version+"("+r.Build+")", 20) + " " + r.Platform)
	}
}

func cmdJwt(f *cliFlags) {
	cfg := resolveCreds(f)
	var tp *jwt.TokenProvider
	var err error
	if cfg.KeyType == uploader.KeyTypeIndividual {
		tp, err = jwt.NewIndividual(cfg.KeyFile, cfg.KeyID)
	} else {
		tp, err = jwt.New(cfg.KeyFile, cfg.KeyID, cfg.IssuerID)
	}
	if err != nil {
		failExit(err.Error())
	}
	tok, err := tp.Token(true)
	if err != nil {
		failExit(err.Error())
	}
	fmt.Println(tok)
	fmt.Println(dim("  （签发于 " + time.Now().Format(time.RFC3339) + "，有效期 18 分钟）"))
}

/* ---------------- 进度条 ---------------- */

// progressBar 的 update 会被多个上传 worker 并发调用，必须加锁。
type progressBar struct {
	mu            sync.Mutex
	isTTY         bool
	lastMilestone int
}

func newProgressBar() *progressBar {
	fi, err := os.Stdout.Stat()
	return &progressBar{isTTY: err == nil && fi.Mode()&os.ModeCharDevice != 0}
}

func (p *progressBar) breakLine() {
	if p.isTTY {
		fmt.Print("\r")
	}
}

func (p *progressBar) update(done, total int64, bps float64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if total <= 0 {
		return
	}
	pct := int(done * 100 / total)
	if p.isTTY {
		const width = 26
		filled := int(done * width / total)
		bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
		line := fmt.Sprintf("\r  %s %3d%%  %s/%s", bar, pct, uploader.FmtBytes(done), uploader.FmtBytes(total))
		if bps > 0 {
			line += "  " + uploader.FmtSpeed(bps)
			if remain := float64(total - done); remain > 0 {
				line += "  ETA " + fmtDuration(remain/bps)
			}
		}
		fmt.Print(line + "    ")
		return
	}
	milestone := pct / 10 * 10
	if milestone > p.lastMilestone {
		p.lastMilestone = milestone
		log("  已上传 " + strconv.Itoa(pct) + "%  (" + uploader.FmtBytes(done) + "/" + uploader.FmtBytes(total) + ")")
	}
}

func (p *progressBar) finish() {
	if p.isTTY {
		fmt.Println()
	}
}

/* ---------------- 帮助文本与 main ---------------- */

var helpLines = []string{
	"",
	"%s v%s — App Store Connect 构建上传工具（纯 Go，Windows/Linux/macOS 可用）",
	"基于官方 buildUploads 接口，无需 Xcode / Transporter / macOS 即可上传 .ipa / .pkg。",
	"",
	"%s: asc-uploader-cli <命令> [选项]",
	"",
	"%s:",
	"  upload   上传 .ipa（或 macOS 的 .pkg）到 App Store Connect",
	"  apps     列出密钥可访问的 App（用于查询 --app-id）",
	"  status   查询某次上传的状态（--id <会话ID>）",
	"  list     列出某 App 的历史上传记录（--app-id <id>）",
	"  jwt      生成并打印 API Token（调试认证用）",
	"  version  显示版本号",
	"",
	"%s（三选一：命令行 > 环境变量 ASC_KEY_FILE/ASC_KEY_ID/ASC_ISSUER_ID > --config 配置文件）:",
	"  --key-file <path>    App Store Connect API 私钥（.p8 文件）路径",
	"  --key-id <id>        密钥 ID（AuthKey_ 文件名后半段）",
	"  --issuer-id <uuid>   Issuer ID（团队密钥必填：页面顶部 UUID；个人密钥不需要）",
	"  --key-type <type>    密钥类型：team（默认）/ individual（个人密钥，认证用 sub=user 无需 Issuer ID）",
	"  --config <path>      JSON 配置文件（keyFile/keyId/issuerId/appId/apiBase）",
	"  --api-base <url>     API 地址（默认 %s，仅测试用）",
	"",
	"%s:",
	"  --ipa <path>         安装包路径（.ipa / .pkg）",
	"  --app-id <id>        App Store Connect 的 App 数字 ID",
	"  --set-version <ver>  覆盖版本号 CFBundleShortVersionString（默认从包内读取）",
	"  --build <num>        覆盖构建号 CFBundleVersion（默认从包内读取）",
	"  --platform <p>       IOS（默认）/ MAC_OS / TV_OS / VISION_OS",
	"  --no-wait            上传字节后不等待 Apple 处理结果",
	"  --poll-interval <s>  轮询间隔秒数（默认 5）",
	"  --wait-timeout <s>   等待处理超时秒数（默认 1800）",
	"  --quiet              减少日志输出",
	"",
	"%s:",
	"  asc-uploader-cli upload --key-file AuthKey_ABC123.p8 --key-id ABC123 ^",
	"      --issuer-id 11111111-2222-3333-4444-555555555555 --app-id 987654321 --ipa MyApp.ipa",
	"  asc-uploader-cli apps --config asc-config.json",
	"  asc-uploader-cli status --id 5f7c9a2e-xxxx",
	"",
	"%s: 0 成功；1 失败；2 上传完成但等待处理超时",
	"",
	"%s（需账户持有人/管理员）:",
	"  App Store Connect 网页 → 用户和访问 → 集成 → App Store Connect API",
	"  → 添加 API 密钥（角色至少 App Manager）→ 下载 .p8 私钥（仅能下载一次）",
	"  → 记录页面上的 Key ID 与 Issuer ID",
	"",
}

func showHelp() {
	fmt.Printf(strings.Join(helpLines, "\n")+"\n",
		bold("asc-uploader-cli"), version.Version,
		bold("用法"), bold("命令"), bold("认证参数"), ascapi.DefaultBase,
		bold("upload 选项"), bold("示例"), bold("退出码"), bold("获取 API 密钥"))
}

func main() {
	if len(os.Args) < 2 {
		showHelp()
		os.Exit(1)
	}
	cmd, args := os.Args[1], os.Args[2:]
	f := newFlags(cmd)
	switch cmd {
	case "help", "-h", "--help":
		showHelp()
		return
	case "version", "-v", "--version":
		fmt.Println("asc-uploader-cli v" + version.Version)
		return
	case "upload", "apps", "status", "list", "jwt":
	default:
		failExit("未知命令: " + cmd + "（可用: upload / apps / status / list / jwt / version）")
	}
	if err := f.fs.Parse(args); err != nil {
		failExit(err.Error())
	}
	if f.fs.NArg() > 0 {
		failExit("无法识别的参数: " + f.fs.Arg(0))
	}
	switch cmd {
	case "upload":
		cmdUpload(f)
	case "apps":
		cmdApps(f)
	case "status":
		cmdStatus(f)
	case "list":
		cmdList(f)
	case "jwt":
		cmdJwt(f)
	}
}
