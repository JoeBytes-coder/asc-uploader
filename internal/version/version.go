package version

// Version 是整个项目的统一版本号（CLI 与 GUI 共用）。
//
// 版本历史：
//
//	v1.x  Node.js 单文件 CLI（asc-upload.mjs）
//	v2.x  Go + Vue 浏览器版（本地 HTTP 服务）
//	v3.x  Wails v2 原生窗口版（WebView2）
//	v4.x  项目化重构：纯 Go CLI + Wails GUI + 完整测试与文档
const Version = "4.1.3"
