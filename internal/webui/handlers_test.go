package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"asc-uploader/internal/mockasc"
	"asc-uploader/internal/testutil"
	"asc-uploader/internal/uploader"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv := New(uploader.NewJobManager())
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts
}

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(body)
	res, err := http.Post(url, "application/json", strings.NewReader(string(raw)))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

// 回归测试：私钥路径无效时 /api/apps 必须返回 400 而不是 panic（历史缺陷）。
func TestAppsInvalidKeyPathReturns400(t *testing.T) {
	_, ts := newTestServer(t)
	code, out := postJSON(t, ts.URL+"/api/apps", map[string]any{
		"keyPath": filepath.Join(t.TempDir(), "missing.p8"),
		"keyId":   "K", "issuerId": "I", "apiBase": ts.URL,
	})
	if code != http.StatusBadRequest {
		t.Fatalf("应返回 400，实际 %d: %v", code, out)
	}
	if msg, _ := out["error"].(string); !strings.Contains(msg, "私钥") && !strings.Contains(msg, "无法读取") {
		t.Errorf("错误信息应说明私钥问题: %v", out)
	}
}

func TestAppsMissingFields(t *testing.T) {
	_, ts := newTestServer(t)
	if code, _ := postJSON(t, ts.URL+"/api/apps", map[string]any{}); code != http.StatusBadRequest {
		t.Fatalf("空参数应 400，实际 %d", code)
	}
}

func TestAppsAgainstMock(t *testing.T) {
	dir := t.TempDir()
	key, err := testutil.NewAPIKey(dir, "T1", "iss-1")
	if err != nil {
		t.Fatal(err)
	}
	mock, err := mockasc.Start(mockasc.Options{PublicKey: &key.PrivateKey.PublicKey, IssuerID: "iss-1", KeyID: "T1"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	_, ts := newTestServer(t)
	code, out := postJSON(t, ts.URL+"/api/apps", map[string]any{
		"keyPath": key.P8Path, "keyId": "T1", "issuerId": "iss-1", "apiBase": mock.URL,
	})
	if code != 200 {
		t.Fatalf("应 200，实际 %d: %v", code, out)
	}
	if apps, _ := out["apps"].([]any); len(apps) != 1 {
		t.Errorf("应列出 1 个 App: %v", out)
	}
}

func TestParsePathValidation(t *testing.T) {
	_, ts := newTestServer(t)
	if code, _ := postJSON(t, ts.URL+"/api/parse-path", map[string]any{"path": ""}); code != 400 {
		t.Fatalf("空路径应 400，实际 %d", code)
	}
	if code, _ := postJSON(t, ts.URL+"/api/parse-path", map[string]any{"path": filepath.Join(t.TempDir(), "no-such.ipa")}); code != 400 {
		t.Fatalf("不可访问路径应 400，实际 %d", code)
	}
}

func TestStartValidation(t *testing.T) {
	_, ts := newTestServer(t)
	if code, _ := postJSON(t, ts.URL+"/api/start", map[string]any{"keyPath": "a", "keyId": "b", "issuerId": "c"}); code != 400 {
		t.Fatalf("缺 App ID 应 400，实际 %d", code)
	}
}

func TestJobSnapshotNil(t *testing.T) {
	_, ts := newTestServer(t)
	res, err := http.Get(ts.URL + "/api/job")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("应 200，实际 %d", res.StatusCode)
	}
	var v any
	if err := json.NewDecoder(res.Body).Decode(&v); err != nil || v != nil {
		t.Errorf("无任务时应输出 null，得到 %v (%v)", v, err)
	}
}

/* ---------------- 本地访问安全校验（WithCORS） ---------------- */

func TestSecurityMiddleware(t *testing.T) {
	srv, _ := newTestServer(t)
	protected := WithCORS(srv.Handler())

	cases := []struct {
		name   string
		origin string
		host   string
		want   int
	}{
		{name: "无 Origin（curl/原生窗口/测试客户端）", want: 200},
		{name: "本机回环 Origin", origin: "http://127.0.0.1:5173", want: 200},
		{name: "localhost Origin（vite dev）", origin: "http://localhost:5173", want: 200},
		{name: "Wails 内嵌页面 Origin", origin: "http://wails.localhost", want: 200},
		{name: "恶意站点 Origin", origin: "https://evil.example.com", want: 403},
		{name: "伪造 Origin（null）", origin: "null", want: 403},
		{name: "DNS rebinding（伪造 Host）", host: "rebind.evil.com", want: 403},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest("GET", "/api/job", nil)
			if tc.host == "" {
				tc.host = "127.0.0.1:0"
			}
			req.Host = tc.host
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			rec := httptest.NewRecorder()
			protected.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
		})
	}
}

func TestHostnameHelpers(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"127.0.0.1:8080", "127.0.0.1"},
		{"localhost:5173", "localhost"},
		{"localhost", "localhost"},
		{"[::1]:9000", "::1"},
	} {
		if got := hostname(c.in); got != c.want {
			t.Errorf("hostname(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if originHostname("http://wails.localhost/path") != "wails.localhost" {
		t.Error("originHostname 解析错误")
	}
}

// 个人密钥：不填 IssuerID 也应通过校验并成功调用（mock 自适应 sub=user）。
func TestAppsIndividualKeyNoIssuer(t *testing.T) {
	dir := t.TempDir()
	key, err := testutil.NewAPIKey(dir, "T2", "iss-2")
	if err != nil {
		t.Fatal(err)
	}
	mock, err := mockasc.Start(mockasc.Options{PublicKey: &key.PrivateKey.PublicKey, IssuerID: "iss-2", KeyID: "T2"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mock.Close() })

	_, ts := newTestServer(t)
	code, out := postJSON(t, ts.URL+"/api/apps", map[string]any{
		"keyPath": key.P8Path, "keyId": "T2", "issuerId": "", "apiBase": mock.URL, "keyType": "individual",
	})
	if code != 200 {
		t.Fatalf("个人密钥（无 IssuerID）应 200，实际 %d: %v", code, out)
	}
}
