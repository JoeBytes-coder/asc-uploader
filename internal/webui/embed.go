package webui

import (
	"embed"
	"io/fs"
	"log"
)

// dist 是 Vite 构建产物（frontend/ → internal/webui/dist）。
// 仓库中保留占位 index.html，保证未构建前端时 go build 仍可通过。
//
//go:embed all:dist
var assets embed.FS

// Dist 返回内嵌的前端静态资源根。
func Dist() fs.FS {
	sub, err := fs.Sub(assets, "dist")
	if err != nil {
		log.Fatalf("内嵌前端资源缺失: %v（请先执行 build.ps1 构建前端再编译）", err)
	}
	return sub
}
