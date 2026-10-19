// Package webui 提供 GUI 版工具的本地服务：
//
//   - 内嵌 Vite 构建的 Element Plus 前端（SPA 静态资源）
//   - /api/* 本地 HTTP 接口（认证测试、安装包解析、上传任务管理）
//   - /api-config.js 运行时注入（原生窗口模式下指向本机回环 API 服务）
//
// 同一套 Handler 同时服务两种运行形态：
//
//	原生窗口（Wails）: 前端由 WebView2 加载，/api 走 127.0.0.1 真实 HTTP 服务
//	浏览器兼容模式  : 同一个端口直接服务前端与 /api（同源）
package webui

import (
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"asc-uploader/internal/uploader"
)

// 复用 uploader 的核心类型（HTTP 层直接编码/解码它们）。
type (
	Task    = uploader.Task
	Config  = uploader.Config
	Request = uploader.Request
	Job     = uploader.Job
)

// Server 聚合本地 API 与静态资源。
type Server struct {
	jobs JobManagerView
	mux  *http.ServeMux
}

// JobManagerView 是 Server 需要的任务管理能力（uploader.JobManager 满足）。
type JobManagerView interface {
	Start(task Task) error
	Cancel()
	Current() *Job
}

// New 构造服务（jobs 由调用方注入，便于测试替换）。
func New(jobs JobManagerView) *Server {
	s := &Server{jobs: jobs, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("POST /api/key-file", s.handleKeyFile)
	m.HandleFunc("POST /api/ipa", s.handleIpaFile)
	m.HandleFunc("POST /api/parse-path", s.handleParsePath)
	m.HandleFunc("POST /api/apps", s.handleApps)
	m.HandleFunc("POST /api/start", s.handleStart)
	m.HandleFunc("GET /api/job", s.handleJob)
	m.HandleFunc("POST /api/cancel", s.handleCancel)
	m.HandleFunc("/api-config.js", func(w http.ResponseWriter, r *http.Request) {
		WriteAPIConfig(w, "") // 默认同源；原生窗口模式由 GUI 入口覆盖
	})
	dfs := Dist()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "index.html"
		}
		if _, err := fs.Stat(dfs, p); err != nil {
			r.URL.Path = "/" // SPA 回退
		}
		http.FileServerFS(dfs).ServeHTTP(w, r)
	})
}

// Handler 返回完整 HTTP Handler（静态资源 + /api），自带本地访问安全校验
// （见 WithCORS：Host 回环 + Origin 白名单），web 模式 / 原生窗口模式统一生效。
func (s *Server) Handler() http.Handler { return WithCORS(s.mux) }

// WithCORS 包一层本地访问安全校验 + 跨域支持：
//
//   - Host 必须是回环地址（127.0.0.1 / localhost / [::1]），防 DNS rebinding
//   - 若带 Origin，必须是本机来源（回环任意端口）或 Wails 内嵌页面来源，
//     防恶意网页对本地服务发起 CSRF（读取私钥路径 / 启动上传等）
//   - 无 Origin 的请求（WebView2 原生窗口、curl、测试）放行
//
// 跨域响应头按请求 Origin 回显（不再使用通配 *）。
func WithCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if origin := r.Header.Get("Origin"); origin != "" {
			if host := originHostname(origin); !localOrigin(host) {
				http.Error(w, "{\"error\":\"origin not allowed\"}", http.StatusForbidden)
				return
			}
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		if host := hostname(r.Host); !localHost(host) {
			http.Error(w, "{\"error\":\"host not allowed\"}", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// hostname 去掉 Host 头里的端口（兼容 IPv6 字面量）。
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return strings.Trim(h, "[]")
	}
	return strings.Trim(strings.Trim(host, "[]"), " ")
}

// originHostname 解析 Origin URL 的主机名。
func originHostname(origin string) string {
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" {
		return ""
	}
	return u.Hostname()
}

// localOrigin 判断 Origin 主机是否属于允许的本地来源。
func localOrigin(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1", "wails.localhost":
		return true
	}
	return false
}

// localHost 判断请求 Host 是否为回环地址（本地服务只应被本机访问）。
func localHost(host string) bool {
	switch strings.ToLower(host) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// WriteAPIConfig 输出 api-config.js：把 API 基地址注入前端（空串表示同源）。
func WriteAPIConfig(w http.ResponseWriter, base string) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	_, _ = w.Write([]byte("window.__ASC_API_BASE = " + strconv.Quote(base) + ";\n"))
}
