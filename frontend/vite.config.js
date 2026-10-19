import { defineConfig } from "vite";
import vue from "@vitejs/plugin-vue";

// base 用相对路径；构建产物输出到 ../internal/webui/dist，
// 由 Go 通过 go:embed 内嵌进 exe（见 internal/webui/embed.go）
export default defineConfig({
  base: "./",
  plugins: [vue()],
  server: {
    proxy: {
      // 开发模式：/api 代理到本机后端（asc-uploader-gui --web --port 17841）
      "/api": "http://127.0.0.1:17841",
    },
  },
  build: {
    outDir: "../internal/webui/dist",
    emptyOutDir: true,
    // Element Plus 全量引入后约 1MB（已按需裁剪图标）；阈值设为略高于实际体积，
    // 超过会重新出现告警而不是被掩盖
    chunkSizeWarningLimit: 1200,
  },
});