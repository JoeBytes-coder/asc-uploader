package webui

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"asc-uploader/internal/ascapi"
	"asc-uploader/internal/ipa"
	"asc-uploader/internal/uploader"
)

/* ---------------- 通用小工具 ---------------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func readBody[T any](r *http.Request) (*T, error) {
	var v T
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(&v); err != nil {
		return nil, fmt.Errorf("请求体解析失败: %w", err)
	}
	return &v, nil
}

// saveMultipartTemp 流式保存上传的文件到临时目录（支持超大 ipa）。
func saveMultipartTemp(r *http.Request, suffix string) (path string, size int64, err error) {
	mr, err := r.MultipartReader()
	if err != nil {
		return "", 0, fmt.Errorf("multipart 读取失败: %w", err)
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", 0, err
		}
		if part.FormName() != "file" {
			_, _ = io.Copy(io.Discard, part)
			continue
		}
		tmp, err := os.CreateTemp("", "asc-ui-*"+suffix)
		if err != nil {
			return "", 0, err
		}
		n, err := io.Copy(tmp, part)
		if cerr := tmp.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			_ = os.Remove(tmp.Name())
			return "", 0, err
		}
		return tmp.Name(), n, nil
	}
	return "", 0, fmt.Errorf("未找到文件字段 file")
}

/* ---------------- /api 接口 ---------------- */

// POST /api/key-file  上传 .p8 私钥到临时目录，返回其路径
func (s *Server) handleKeyFile(w http.ResponseWriter, r *http.Request) {
	path, _, err := saveMultipartTemp(r, ".p8")
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"path": path})
}

// POST /api/ipa?name=xxx.ipa  上传安装包（拖拽），保存并解析
func (s *Server) handleIpaFile(w http.ResponseWriter, r *http.Request) {
	qname := r.URL.Query().Get("name")
	path, size, err := saveMultipartTemp(r, filepath.Ext(qname))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	name := filepath.Base(path)
	if qname != "" {
		name = qname
	}
	resp := map[string]any{"path": path, "fileName": name, "size": size, "info": nil}
	if info, perr := ipa.ReadInfo(path); perr == nil {
		resp["info"] = info
	} else {
		resp["warn"] = "读取包信息失败: " + perr.Error()
	}
	writeJSON(w, 200, resp)
}

type parsePathReq struct {
	Path string `json:"path"`
}

// POST /api/parse-path  按本机路径解析安装包
func (s *Server) handleParsePath(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[parsePathReq](r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.Path == "" {
		writeErr(w, 400, "路径为空")
		return
	}
	st, err := os.Stat(req.Path)
	if err != nil {
		writeErr(w, 400, "无法访问路径: "+err.Error())
		return
	}
	resp := map[string]any{"path": req.Path, "fileName": filepath.Base(req.Path), "size": st.Size(), "info": nil}
	if info, perr := ipa.ReadInfo(req.Path); perr == nil {
		resp["info"] = info
	} else {
		resp["warn"] = "读取包信息失败: " + perr.Error()
	}
	writeJSON(w, 200, resp)
}

type appsReq struct {
	KeyPath  string `json:"keyPath"`
	KeyId    string `json:"keyId"`
	IssuerId string `json:"issuerId"` // 团队密钥必填；个人密钥不使用（JWT 用 sub=user）
	ApiBase  string `json:"apiBase"`
	KeyType  string `json:"keyType"` // team（默认）/ individual
}

// POST /api/apps  测试连接并列出可访问的 App
func (s *Server) handleApps(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[appsReq](r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	keyType := uploader.NormalizeKeyType(req.KeyType)
	if req.KeyPath == "" || req.KeyId == "" {
		writeErr(w, 400, "请先填写私钥文件与 Key ID")
		return
	}
	if keyType == uploader.KeyTypeTeam && req.IssuerId == "" {
		writeErr(w, 400, "团队密钥必须填写 Issuer ID（如使用个人密钥，请把密钥类型切换为「个人密钥」——它不需要 Issuer ID）")
		return
	}
	client, ok := mustClient(w, req.KeyPath, req.KeyId, req.IssuerId, req.ApiBase, keyType)
	if !ok {
		return // mustClient 已写好 400 响应
	}
	apps, err := ascapi.ListApps(r.Context(), client)
	if err != nil {
		writeErr(w, 502, friendly(err))
		return
	}
	writeJSON(w, 200, map[string]any{"apps": apps})
}

type startReq struct {
	KeyPath  string `json:"keyPath"`
	KeyId    string `json:"keyId"`
	IssuerId string `json:"issuerId"`
	ApiBase  string `json:"apiBase"`
	KeyType  string `json:"keyType"` // team（默认）/ individual；两种密钥协议相同，仅凭据来源不同
	AppId    string `json:"appId"`
	IpaPath  string `json:"ipaPath"`
	Version  string `json:"version"`
	Build    string `json:"build"`
	Platform string `json:"platform"`
	Wait     bool   `json:"wait"`
}

// POST /api/start  启动后台上传任务
func (s *Server) handleStart(w http.ResponseWriter, r *http.Request) {
	req, err := readBody[startReq](r)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if req.KeyPath == "" || req.KeyId == "" {
		writeErr(w, 400, "缺少认证信息（私钥 / Key ID）")
		return
	}
	if uploader.NormalizeKeyType(req.KeyType) == uploader.KeyTypeTeam && req.IssuerId == "" {
		writeErr(w, 400, "团队密钥必须填写 Issuer ID（个人密钥不需要）")
		return
	}
	if req.AppId == "" {
		writeErr(w, 400, "缺少 App ID")
		return
	}
	if _, err := os.Stat(req.IpaPath); err != nil {
		writeErr(w, 400, "安装包不可访问: "+err.Error())
		return
	}
	if req.Platform == "" {
		req.Platform = "IOS"
	}
	if err := s.jobs.Start(Task{
		Config: Config{
			KeyPath: req.KeyPath, KeyID: req.KeyId, IssuerID: req.IssuerId, APIBase: req.ApiBase,
			KeyType: uploader.NormalizeKeyType(req.KeyType),
		},
		Request: Request{
			AppID: req.AppId, IpaPath: req.IpaPath, Version: req.Version,
			Build: req.Build, Platform: req.Platform, Wait: req.Wait,
		},
	}); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// GET /api/job  查询当前任务快照
func (s *Server) handleJob(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.jobs.Current().Snapshot())
}

// POST /api/cancel  取消当前任务
func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	s.jobs.Cancel()
	writeJSON(w, 200, map[string]bool{"ok": true})
}

/* ---------------- 辅助 ---------------- */

// mustClient 构造 API 客户端；失败时写出 400 并返回 ok=false，调用方必须立即 return。
func mustClient(w http.ResponseWriter, keyPath, keyID, issuerID, apiBase, keyType string) (*ascapi.Client, bool) {
	c, err := ascapi.NewClient(ascapi.Credentials{KeyPath: keyPath, KeyID: keyID, IssuerID: issuerID, APIBase: apiBase, KeyType: keyType})
	if err != nil {
		writeErr(w, 400, err.Error())
		return nil, false
	}
	return c, true
}

// friendly 把 ASC 错误转成带中文提示的文本。
func friendly(err error) string {
	if ae, ok := err.(*ascapi.Error); ok {
		return ae.WithHint()
	}
	return err.Error()
}
