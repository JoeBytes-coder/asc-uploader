// asc-uploader-gui 是原生窗口版（Wails v2 + WebView2）图形界面入口。
//
// 运行形态：
//
//	默认      原生窗口：前端由 WebView2 内嵌渲染，不启动外部浏览器；
//	          /api 走本机回环 HTTP 服务（WebView2 的请求拦截对大文件
//	          multipart 不可靠，改走真实 socket 后拖拽上传 .ipa 稳定可靠）
//	--web     浏览器兼容模式：本机 HTTP 服务 + 系统浏览器（兜底，
//	          供无 WebView2 运行时的旧机器，功能完全相同）
//
// 启动加固（普通权限可用，无需管理员）：
//
//  1. WebView2 运行时预检，缺失时弹窗给出微软官方安装链接
//  2. 数据目录多候选 + 真实写探测（LOCALAPPDATA → APPDATA → exe 旁 → 临时目录）
//  3. 任何启动失败都会弹窗说明（GUI 程序的 stderr 无人可见，错误必须弹窗）
//  4. 弹窗支持一键降级到 --web 模式
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/wailsapp/go-webview2/webviewloader"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"

	"asc-uploader/internal/uploader"
	"asc-uploader/internal/version"
	"asc-uploader/internal/webui"
)

func main() {
	webMode := flag.Bool("web", false, "兼容模式：启动本机 HTTP 服务并调用系统浏览器（默认为原生窗口，不使用外部浏览器）")
	port := flag.Int("port", 0, "（web 模式）监听端口，0 为随机")
	noOpen := flag.Bool("no-open", false, "（web 模式）不自动打开浏览器")
	flag.Parse()

	server := webui.New(uploader.NewJobManager())

	if *webMode {
		runWebMode(server.Handler(), *port, !*noOpen)
		return
	}

	// 1) 预检 WebView2 运行时（Win10/11 一般自带）
	if ver, err := webviewloader.GetAvailableCoreWebView2BrowserVersionString(""); err != nil || ver == "" {
		offerWebFallback("未检测到 WebView2 运行时。\n\n可从微软官网免费安装（用户级安装即可，无需管理员）:\nhttps://developer.microsoft.com/microsoft-edge/webview2/")
		return
	}

	// 2) 选一个当前用户真正可写的数据目录（多候选 + 写探测，普通权限即可）
	dataDir, err := pickWebView2DataDir()
	if err != nil {
		offerWebFallback(err.Error())
		return
	}

	// 3) 原生窗口：/api 走真实本机 HTTP 服务
	//    （WebView2 的 WebResourceRequested 拦截转发大文件 multipart 不可靠）
	apiLn, apiErr := net.Listen("tcp", "127.0.0.1:0")
	if apiErr != nil {
		offerWebFallback("无法启动本机 API 服务: " + apiErr.Error())
		return
	}
	apiBase := "http://127.0.0.1:" + strconv.Itoa(apiLn.Addr().(*net.TCPAddr).Port)
	go func() { _ = http.Serve(apiLn, server.Handler()) }() // Handler 已自带安全校验

	winOpts := &windows.Options{
		WebviewIsTransparent: false,
		WindowIsTranslucent:  false,
	}
	winOpts.WebviewUserDataPath = dataDir
	if err := wails.Run(&options.App{
		Title:     "iOS 安装包上传工具 · App Store Connect",
		Width:     1020,
		Height:    760, // 适配高分屏（1.75x 缩放的 1440p 屏逻辑高约 822）
		MinWidth:  780,
		MinHeight: 620,
		AssetServer: &assetserver.Options{
			Assets: webui.Dist(),
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/api-config.js" {
					// 把本机回环 API 地址注入前端（前端据此发起绝对请求）
					webui.WriteAPIConfig(w, apiBase)
					return
				}
				server.Handler().ServeHTTP(w, r)
			}),
		},
		Windows: winOpts,
	}); err != nil {
		offerWebFallback(fmt.Sprintf("窗口启动失败: %v", err))
	}
}

// pickWebView2DataDir 多候选 + 真实写探测，保证普通权限下也能找到可用目录。
func pickWebView2DataDir() (string, error) {
	var candidates []string
	if v := os.Getenv("ASC_UI_WEBVIEW_DATA"); v != "" {
		candidates = append(candidates, v)
	}
	if d, err := os.UserCacheDir(); err == nil {
		candidates = append(candidates, filepath.Join(d, "asc-uploader-ui"))
	}
	if d, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(d, "asc-uploader-ui"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), ".webview2-data"))
	}
	candidates = append(candidates, filepath.Join(os.TempDir(), "asc-uploader-ui-webview"))
	for _, dir := range candidates {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			continue
		}
		probe := filepath.Join(dir, ".write-probe")
		if err := os.WriteFile(probe, []byte("ok"), 0o644); err != nil {
			continue
		}
		_ = os.Remove(probe)
		return dir, nil
	}
	return "", fmt.Errorf("找不到当前用户可写的 WebView2 数据目录（尝试过: %s）。\n请把程序放到普通目录（如桌面）后再试，或以参数 --web 运行。", strings.Join(candidates, "; "))
}

/* ---------------- 弹窗（GUI 程序的 stderr 无人可见，错误必须弹窗） ---------------- */

var (
	user32DLL       = syscall.NewLazyDLL("user32.dll")
	procMessageBoxW = user32DLL.NewProc("MessageBoxW")
)

const (
	mbYesNo     = 0x00000004
	mbIconError = 0x00000010
	idYes       = 6
)

func messageBox(title, text string, flags uint32) int32 {
	t16, _ := syscall.UTF16PtrFromString(text)
	c16, _ := syscall.UTF16PtrFromString(title)
	ret, _, _ := procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t16)), uintptr(unsafe.Pointer(c16)), uintptr(flags))
	return int32(ret)
}

// offerWebFallback 启动失败时给出明确弹窗，并允许一键降级到浏览器兼容模式。
func offerWebFallback(reason string) {
	msg := reason + "\n\n是否改用「浏览器兼容模式」继续？（上传功能完全相同）"
	if messageBox("asc-uploader-ui 窗口模式启动失败", msg, mbYesNo|mbIconError) == idYes {
		relaunchWebMode()
	}
}

func relaunchWebMode() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "--web")
	_ = cmd.Start()
	os.Exit(0)
}

// runWebMode 浏览器兼容模式：本机 HTTP + 系统浏览器（兜底）。
func runWebMode(handler http.Handler, port int, open bool) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+strconv.Itoa(port))
	if err != nil {
		log.Fatalf("监听失败: %v", err)
	}
	addr := ln.Addr().(*net.TCPAddr)
	url := "http://127.0.0.1:" + strconv.Itoa(addr.Port)
	fmt.Printf("asc-uploader-ui v%s（web 兼容模式）\n已启动: %s\n浏览器没有自动打开时，请手动访问上面的地址；关闭本窗口即退出。\n", version.Version, url)
	if open {
		go func() {
			time.Sleep(300 * time.Millisecond)
			openBrowser(url)
		}()
	}
	log.Fatal(http.Serve(ln, handler))
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("cmd", "/c", "start", "", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
