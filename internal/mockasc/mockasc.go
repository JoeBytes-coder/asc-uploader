// Package mockasc 在本机完整模拟 App Store Connect API 的构建上传全流程，
// 用于离线端到端测试（不联网、不消耗真实 API 配额）。
//
// 模拟范围与真实接口逐条对应：
//
//	GET  /v1/apps                     列出 App
//	POST /v1/buildUploads             创建上传会话（校验请求体必填字段）
//	POST /v1/buildUploadFiles         预留文件位（按 PartSize 切片生成 uploadOperations）
//	PUT  /upload-op/{fileID}/{part}   分片字节上传（校验请求头与长度，禁止携带 JWT）
//	PATCH /v1/buildUploadFiles/{id}   提交文件（拼接分片、比对 MD5）
//	GET  /v1/buildUploads/{id}        状态轮转 AWAITING_UPLOAD→PROCESSING→COMPLETE
//	GET  /v1/apps/{id}/buildUploads   历史上传列表
//
// 所有需要鉴权的端点都做 ES256 JWT 验签（kid/iss/aud/exp/有效期上限/签名）。
package mockasc

import (
	"crypto/ecdsa"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Options 配置模拟服务器。IssuerID 仅用于校验团队密钥 token（个人密钥 token 无 iss、sub="user"）。
type Options struct {
	PublicKey         *ecdsa.PublicKey // 验签公钥（与客户端 .p8 私钥对应）
	IssuerID          string
	KeyID             string
	PartSize          int64 // 分片大小（字节）；测试用小值强制多分片，0 表示 1MiB
	ProcessAfterPolls int   // 提交后第 N 次状态查询转为 COMPLETE（默认 2）
}

// Server 是运行中的模拟服务器。
type Server struct {
	URL string

	srv  *http.Server
	ln   net.Listener
	opts Options

	mu         sync.Mutex
	violations []string
	uploads    map[string]*mockUpload
	files      map[string]*mockFile
	buSeq      int
	bufSeq     int
}

type mockUpload struct {
	id       string
	appID    string
	version  string
	build    string
	platform string
	state    string // AWAITING_UPLOAD / PROCESSING / COMPLETE / FAILED
	polls    int
	fileID   string
	buildID  string
}

type mockFile struct {
	id            string
	buildUploadID string
	fileName      string
	fileSize      int64
	uti           string
	parts         map[int][]byte
	committed     bool
}

// Start 在 127.0.0.1 随机端口启动模拟服务器。
func Start(opts Options) (*Server, error) {
	if opts.PartSize <= 0 {
		opts.PartSize = 1 << 20
	}
	if opts.ProcessAfterPolls <= 0 {
		opts.ProcessAfterPolls = 2
	}
	s := &Server{
		opts:    opts,
		uploads: map[string]*mockUpload{},
		files:   map[string]*mockFile{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handle)
	s.srv = &http.Server{Handler: mux}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.ln = ln
	s.URL = "http://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
	go func() { _ = s.srv.Serve(ln) }()
	return s, nil
}

// Close 停止服务器。
func (s *Server) Close() error { return s.srv.Close() }

// Violations 返回协议违规记录（应始终为空）。
func (s *Server) Violations() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.violations...)
}

func (s *Server) violate(format string, args ...any) {
	s.violations = append(s.violations, fmt.Sprintf(format, args...))
}

/* ---------------- JWT 验签 ---------------- */

type jwtClaims struct {
	Iss string `json:"iss"` // 团队密钥 payload
	Sub string `json:"sub"` // 个人密钥 payload（应为 "user"）
	Iat int64  `json:"iat"`
	Exp int64  `json:"exp"`
	Aud string `json:"aud"`
}

func (s *Server) verifyJWT(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	token := ""
	if len(auth) > 7 && (auth[:7] == "Bearer " || auth[:7] == "bearer ") {
		token = auth[7:]
	}
	parts := splitDot(token)
	if len(parts) != 3 {
		return "token 格式错误", false
	}
	b64 := base64.RawURLEncoding
	headerRaw, err1 := b64.DecodeString(parts[0])
	payloadRaw, err2 := b64.DecodeString(parts[1])
	sig, err3 := b64.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return "token base64 解码失败", false
	}
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	var claims jwtClaims
	if json.Unmarshal(headerRaw, &header) != nil || json.Unmarshal(payloadRaw, &claims) != nil {
		return "token 解析失败", false
	}
	if header.Alg != "ES256" {
		return "alg 必须是 ES256，收到 " + header.Alg, false
	}
	if header.Kid != s.opts.KeyID {
		return "Key ID 不匹配（期望 " + s.opts.KeyID + "）", false
	}
	// 官方规则：团队密钥 payload 用 iss；个人密钥不用 iss、用 sub="user"
	switch {
	case claims.Sub == "user" && claims.Iss == "":
		// 个人密钥 token：合法，跳过 Issuer 校验
	case claims.Sub == "" && claims.Iss != "":
		if claims.Iss != s.opts.IssuerID {
			return "Issuer ID 不匹配", false
		}
	default:
		return "payload 既无有效 iss（团队密钥）也不是 sub=user（个人密钥）", false
	}
	if claims.Aud != "appstoreconnect-v1" {
		return "aud 不是 appstoreconnect-v1", false
	}
	now := time.Now().Unix()
	if claims.Exp <= now {
		return "token 已过期", false
	}
	if claims.Exp-claims.Iat > 20*60 {
		return "token 有效期超过 20 分钟上限", false
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r_ := new(big.Int).SetBytes(sig[:32])
	s_ := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(s.opts.PublicKey, sum[:], r_, s_) {
		return "ES256 签名校验失败", false
	}
	return "", true
}

func splitDot(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	out = append(out, s[start:])
	return out
}

/* ---------------- HTTP 处理 ---------------- */

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errShape(status int, code, title, detail string) map[string]any {
	return map[string]any{
		"errors": []map[string]any{{
			"id": fmt.Sprintf("err-%d", status), "status": strconv.Itoa(status),
			"code": code, "title": title, "detail": detail,
		}},
	}
}

func (s *Server) needAuth(w http.ResponseWriter, r *http.Request) bool {
	if why, ok := s.verifyJWT(r); !ok {
		writeJSON(w, 401, errShape(401, "NOT_AUTHORIZED", "认证失败", why))
		return false
	}
	return true
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	defer r.Body.Close()

	switch {
	case r.Method == "GET" && r.URL.Path == "/v1/apps":
		if !s.needAuth(w, r) {
			return
		}
		writeJSON(w, 200, map[string]any{
			"data": []map[string]any{{
				"id": "123456789", "type": "apps",
				"attributes": map[string]any{"name": "Mock 测试应用", "bundleId": "com.example.demo"},
			}},
		})

	case r.Method == "POST" && r.URL.Path == "/v1/buildUploads":
		if !s.needAuth(w, r) {
			return
		}
		var body0 struct {
			Data struct {
				Type       string `json:"type"`
				Attributes struct {
					CFBundleShortVersionString string `json:"cfBundleShortVersionString"`
					CFBundleVersion            string `json:"cfBundleVersion"`
					Platform                   string `json:"platform"`
				} `json:"attributes"`
				Relationships struct {
					App struct {
						Data struct {
							ID string `json:"id"`
						} `json:"data"`
					} `json:"app"`
				} `json:"relationships"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &body0)
		d := body0.Data
		var problems []string
		if d.Type != "buildUploads" {
			problems = append(problems, "data.type 必须是 buildUploads")
		}
		if d.Attributes.CFBundleShortVersionString == "" {
			problems = append(problems, "缺少 cfBundleShortVersionString")
		}
		if d.Attributes.CFBundleVersion == "" {
			problems = append(problems, "缺少 cfBundleVersion")
		}
		if d.Attributes.Platform == "" {
			problems = append(problems, "缺少 platform")
		}
		if d.Relationships.App.Data.ID == "" {
			problems = append(problems, "缺少 relationships.app")
		}
		if len(problems) > 0 {
			writeJSON(w, 409, errShape(409, "ENTITY_ERROR", "请求体不合法", join(problems, "; ")))
			return
		}
		s.mu.Lock()
		s.buSeq++
		id := fmt.Sprintf("bu-%d", s.buSeq)
		s.uploads[id] = &mockUpload{
			id: id, appID: d.Relationships.App.Data.ID,
			version:  d.Attributes.CFBundleShortVersionString,
			build:    d.Attributes.CFBundleVersion,
			platform: d.Attributes.Platform,
			state:    "AWAITING_UPLOAD",
		}
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{
			"data": map[string]any{
				"id": id, "type": "buildUploads",
				"attributes": map[string]any{
					"platform":                   d.Attributes.Platform,
					"cfBundleShortVersionString": d.Attributes.CFBundleShortVersionString,
					"cfBundleVersion":            d.Attributes.CFBundleVersion,
					"createdDate":                time.Now().UTC().Format(time.RFC3339),
					"state":                      map[string]any{"state": "AWAITING_UPLOAD"},
				},
				"relationships": map[string]any{
					"app": map[string]any{"data": map[string]any{"type": "apps", "id": d.Relationships.App.Data.ID}},
				},
			},
		})

	case r.Method == "POST" && r.URL.Path == "/v1/buildUploadFiles":
		if !s.needAuth(w, r) {
			return
		}
		var body0 struct {
			Data struct {
				Type       string `json:"type"`
				Attributes struct {
					AssetType string `json:"assetType"`
					FileName  string `json:"fileName"`
					FileSize  int64  `json:"fileSize"`
					UTI       string `json:"uti"`
				} `json:"attributes"`
				Relationships struct {
					BuildUpload struct {
						Data struct {
							ID string `json:"id"`
						} `json:"data"`
					} `json:"buildUpload"`
				} `json:"relationships"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &body0)
		d := body0.Data
		buID := d.Relationships.BuildUpload.Data.ID
		s.mu.Lock()
		bu, ok := s.uploads[buID]
		if !ok {
			s.mu.Unlock()
			writeJSON(w, 404, errShape(404, "NOT_FOUND", "资源不存在", "buildUpload "+buID))
			return
		}
		if d.Type != "buildUploadFiles" || d.Attributes.FileName == "" || d.Attributes.FileSize == 0 || d.Attributes.UTI == "" || d.Attributes.AssetType != "ASSET" {
			s.mu.Unlock()
			writeJSON(w, 409, errShape(409, "ENTITY_ERROR", "请求体不合法", "attributes 校验失败"))
			return
		}
		s.bufSeq++
		id := fmt.Sprintf("buf-%d", s.bufSeq)
		partSize := s.opts.PartSize
		baseURL := s.URL
		var ops []map[string]any
		for off, part := int64(0), 1; off < d.Attributes.FileSize; off, part = off+partSize, part+1 {
			length := d.Attributes.FileSize - off
			if length > partSize {
				length = partSize
			}
			ops = append(ops, map[string]any{
				"method": "PUT",
				"url":    fmt.Sprintf("%s/upload-op/%s/%d", baseURL, id, part),
				"length": length, "offset": off, "partNumber": part,
				"requestHeaders": []map[string]any{{"name": "Content-Type", "value": "application/octet-stream"}},
			})
		}
		s.files[id] = &mockFile{
			id: id, buildUploadID: bu.id, fileName: d.Attributes.FileName,
			fileSize: d.Attributes.FileSize, uti: d.Attributes.UTI, parts: map[int][]byte{},
		}
		bu.fileID = id
		s.mu.Unlock()
		writeJSON(w, 201, map[string]any{
			"data": map[string]any{
				"id": id, "type": "buildUploadFiles",
				"attributes": map[string]any{
					"fileName": d.Attributes.FileName, "fileSize": d.Attributes.FileSize,
					"uti": d.Attributes.UTI, "uploadOperations": ops,
				},
				"relationships": map[string]any{
					"buildUpload": map[string]any{"data": map[string]any{"type": "buildUploads", "id": buID}},
				},
			},
		})

	case r.Method == "PUT" && hasPrefix(r.URL.Path, "/upload-op/"):
		// /upload-op/{fileID}/{part} 手工解析（fmt.Sscanf 的 %s 会贪婪吞掉斜杠）
		segs := splitPath(r.URL.Path) // ["upload-op", fileID, part]
		if len(segs) != 3 {
			writeJSON(w, 404, errShape(404, "NOT_FOUND", "路径不存在", r.URL.Path))
			return
		}
		fileID := segs[1]
		partNo, err := strconv.Atoi(segs[2])
		if err != nil {
			writeJSON(w, 404, errShape(404, "NOT_FOUND", "路径不存在", r.URL.Path))
			return
		}
		s.mu.Lock()
		file := s.files[fileID]
		s.mu.Unlock()
		if file == nil {
			writeJSON(w, 404, errShape(404, "NOT_FOUND", "文件位不存在", fileID))
			return
		}
		if r.Header.Get("Authorization") != "" {
			s.violate("upload-op 携带了 Authorization 头（预签名 URL 不应带 JWT）")
			writeJSON(w, 400, errShape(400, "BAD_HEADER", "头不合法", "预签名 URL 不应携带 Authorization"))
			return
		}
		if r.Header.Get("Content-Type") != "application/octet-stream" {
			s.violate("upload-op %d 的 Content-Type 不是指定的 application/octet-stream", partNo)
			writeJSON(w, 400, errShape(400, "BAD_HEADER", "头不合法", "Content-Type 必须遵守 requestHeaders"))
			return
		}
		s.mu.Lock()
		var expected int64 = -1
		for off, part := int64(0), 1; off < file.fileSize; off, part = off+s.opts.PartSize, part+1 {
			length := file.fileSize - off
			if length > s.opts.PartSize {
				length = s.opts.PartSize
			}
			if part == partNo {
				expected = length
				break
			}
		}
		s.mu.Unlock()
		if expected < 0 || int64(len(body)) != expected {
			s.violate("upload-op %d 字节数不符：收到 %d，应为 %d", partNo, len(body), expected)
			writeJSON(w, 400, errShape(400, "BAD_LENGTH", "长度不合法", "分片字节数不符"))
			return
		}
		s.mu.Lock()
		file.parts[partNo] = append([]byte(nil), body...)
		s.mu.Unlock()
		w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", partNo))
		w.WriteHeader(200)

	case r.Method == "PATCH" && hasPrefix(r.URL.Path, "/v1/buildUploadFiles/"):
		if !s.needAuth(w, r) {
			return
		}
		fileID := r.URL.Path[len("/v1/buildUploadFiles/"):]
		s.mu.Lock()
		file := s.files[fileID]
		if file == nil {
			s.mu.Unlock()
			writeJSON(w, 404, errShape(404, "NOT_FOUND", "文件位不存在", fileID))
			return
		}
		var body0 struct {
			Data struct {
				Attributes struct {
					Uploaded            bool `json:"uploaded"`
					SourceFileChecksums struct {
						File struct {
							Algorithm string `json:"algorithm"`
							Hash      string `json:"hash"`
						} `json:"file"`
					} `json:"sourceFileChecksums"`
				} `json:"attributes"`
			} `json:"data"`
		}
		_ = json.Unmarshal(body, &body0)
		if !body0.Data.Attributes.Uploaded {
			s.mu.Unlock()
			writeJSON(w, 409, errShape(409, "ENTITY_ERROR", "提交不完整", "uploaded 必须为 true"))
			return
		}
		var ordered []byte
		for i := 1; ; i++ {
			p, ok := file.parts[i]
			if !ok {
				break
			}
			ordered = append(ordered, p...)
		}
		if int64(len(ordered)) != file.fileSize {
			s.mu.Unlock()
			writeJSON(w, 409, errShape(409, "ENTITY_ERROR", "分片不完整", fmt.Sprintf("共 %d 字节，应为 %d", len(ordered), file.fileSize)))
			return
		}
		sum := md5.Sum(ordered)
		claimed := body0.Data.Attributes.SourceFileChecksums.File.Hash
		if body0.Data.Attributes.SourceFileChecksums.File.Algorithm != "MD5" || claimed != fmt.Sprintf("%x", sum) {
			s.mu.Unlock()
			writeJSON(w, 409, errShape(409, "ENTITY_ERROR", "校验和不匹配", fmt.Sprintf("服务端 %x，客户端 %s", sum, claimed)))
			return
		}
		file.committed = true
		s.uploads[file.buildUploadID].state = "PROCESSING"
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{
			"data": map[string]any{
				"id": file.id, "type": "buildUploadFiles",
				"attributes": map[string]any{
					"fileName": file.fileName, "fileSize": file.fileSize,
					"assetDeliveryState": map[string]any{"state": "UPLOAD_COMPLETE"},
				},
			},
		})

	case r.Method == "GET" && hasPrefix(r.URL.Path, "/v1/buildUploads/"):
		if !s.needAuth(w, r) {
			return
		}
		id := r.URL.Path[len("/v1/buildUploads/"):]
		s.mu.Lock()
		bu := s.uploads[id]
		if bu == nil {
			s.mu.Unlock()
			writeJSON(w, 404, errShape(404, "NOT_FOUND", "会话不存在", id))
			return
		}
		file := s.files[bu.fileID]
		if file != nil && file.committed {
			bu.polls++
			if bu.polls >= s.opts.ProcessAfterPolls {
				bu.state = "COMPLETE"
				bu.buildID = "b-" + bu.id
			}
		}
		resp := map[string]any{
			"data": map[string]any{
				"id": bu.id, "type": "buildUploads",
				"attributes": map[string]any{
					"platform":                   bu.platform,
					"cfBundleShortVersionString": bu.version,
					"cfBundleVersion":            bu.build,
					"createdDate":                time.Now().UTC().Format(time.RFC3339),
					"state":                      map[string]any{"state": bu.state},
				},
				"relationships": map[string]any{},
			},
			"included": []any{},
		}
		if bu.buildID != "" {
			resp["data"].(map[string]any)["relationships"] = map[string]any{
				"build": map[string]any{"data": map[string]any{"type": "builds", "id": bu.buildID}},
			}
			if contains(r.URL.Query().Get("include"), "build") {
				resp["included"] = []map[string]any{{
					"id": bu.buildID, "type": "build",
					"attributes": map[string]any{"version": bu.version, "processingState": "VALID"},
				}}
			}
		}
		s.mu.Unlock()
		writeJSON(w, 200, resp)

	case r.Method == "GET" && hasSuffix(r.URL.Path, "/buildUploads"):
		if !s.needAuth(w, r) {
			return
		}
		appID := r.URL.Path[len("/v1/apps/"):]
		appID = appID[:len(appID)-len("/buildUploads")]
		s.mu.Lock()
		rows := []map[string]any{}
		for _, bu := range s.uploads {
			if bu.appID != appID {
				continue
			}
			rows = append(rows, map[string]any{
				"id": bu.id, "type": "buildUploads",
				"attributes": map[string]any{
					"platform":                   bu.platform,
					"cfBundleShortVersionString": bu.version,
					"cfBundleVersion":            bu.build,
					"createdDate":                time.Now().UTC().Format(time.RFC3339),
					"state":                      map[string]any{"state": bu.state},
				},
			})
		}
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"data": rows})

	default:
		writeJSON(w, 404, errShape(404, "NOT_FOUND", "路径不存在", r.Method+" "+r.URL.Path))
	}
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
func hasSuffix(s, p string) bool { return len(s) >= len(p) && s[len(s)-len(p):] == p }

// splitPath 把 "/a/b/c" 切成 ["a","b","c"]。
func splitPath(p string) []string {
	var out []string
	for _, seg := range strings.Split(p, "/") {
		if seg != "" {
			out = append(out, seg)
		}
	}
	return out
}
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
