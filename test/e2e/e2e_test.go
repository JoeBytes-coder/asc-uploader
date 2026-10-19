// 端到端测试：真实走完「IPA 解析 → JWT 验签 → 创建会话 → 分片上传 →
// MD5 提交 → 状态轮询 → COMPLETE」全流程，全程对着本机 mockasc 模拟服务器，
// 不联网、不消耗真实 API 配额。
package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"asc-uploader/internal/ascapi"
	"asc-uploader/internal/mockasc"
	"asc-uploader/internal/testutil"
	"asc-uploader/internal/uploader"
	"asc-uploader/internal/webui"
)

func setup(t *testing.T) (cfg uploader.Config, ipaPath string, mock *mockasc.Server) {
	t.Helper()
	dir := t.TempDir()
	key, err := testutil.NewAPIKey(dir, "TESTKEY001", "11111111-2222-3333-4444-555555555555")
	if err != nil {
		t.Fatal(err)
	}
	mock, err = mockasc.Start(mockasc.Options{
		PublicKey: &key.PrivateKey.PublicKey,
		IssuerID:  key.IssuerID,
		KeyID:     key.KeyID,
		PartSize:  7, // 每片 7 字节，强制多分片并发
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mock.Close() })
	ipaPath, err = testutil.BuildIPA(dir, map[string]any{
		"CFBundleIdentifier":         "com.example.demo",
		"CFBundleName":               "E2E应用",
		"CFBundleShortVersionString": "2.0.1",
		"CFBundleVersion":            "777",
	})
	if err != nil {
		t.Fatal(err)
	}
	return uploader.Config{
		KeyPath: key.P8Path, KeyID: key.KeyID, IssuerID: key.IssuerID, APIBase: mock.URL,
	}, ipaPath, mock
}

// TestUploadPipelineFullFlow 直接驱动 uploader.Run 走完整流程。
func TestUploadPipelineFullFlow(t *testing.T) {
	cfg, ipaPath, mock := setup(t)
	var phases []string
	var progressCalls atomic.Int64
	res, err := uploader.Run(context.Background(), cfg, uploader.Request{
		AppID: "123456789", IpaPath: ipaPath, Platform: "IOS", Wait: true,
		PollInterval: 30 * time.Millisecond, WaitTimeout: 10 * time.Second,
	}, uploader.Hooks{
		Phase: func(p string) { phases = append(phases, p) },
		// 并发进度回调驱动 speedEMA（多 worker 写），配合 -race 验证加锁正确性
		Progress: func(done, total int64, bps float64) { progressCalls.Add(1) },
	})
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if res.BuildUploadID == "" {
		t.Error("缺少 BuildUploadID")
	}
	if res.BuildID == "" {
		t.Error("等待处理后应返回 BuildID")
	}
	if progressCalls.Load() == 0 {
		t.Error("并发进度回调未被调用")
	}
	if !strings.HasPrefix(res.BuildID, "b-bu-") {
		t.Errorf("BuildID 形态错误: %s", res.BuildID)
	}
	wantPhases := []string{"prepare", "session", "reserve", "upload", "commit", "wait"}
	if strings.Join(phases, ",") != strings.Join(wantPhases, ",") {
		t.Errorf("阶段顺序错误: %v", phases)
	}
	if v := mock.Violations(); len(v) > 0 {
		t.Errorf("协议违规: %v", v)
	}
}

// TestUploadPipelineNoWait 验证 --no-wait 路径：字节上传完成即返回。
func TestUploadPipelineNoWait(t *testing.T) {
	cfg, ipaPath, mock := setup(t)
	res, err := uploader.Run(context.Background(), cfg, uploader.Request{
		AppID: "123456789", IpaPath: ipaPath, Wait: false,
	}, uploader.Hooks{})
	if err != nil {
		t.Fatalf("上传失败: %v", err)
	}
	if res.BuildID != "" {
		t.Error("no-wait 不应等待出 BuildID")
	}
	if v := mock.Violations(); len(v) > 0 {
		t.Errorf("协议违规: %v", v)
	}
}

// TestUploadPipelineBadKey 验证密钥不匹配时的失败链路（401 → 明确报错）。
func TestUploadPipelineBadKey(t *testing.T) {
	cfg, ipaPath, _ := setup(t)
	cfg.KeyID = "WRONGKEY" // kid 与 mock 期望不符 → 401
	_, err := uploader.Run(context.Background(), cfg, uploader.Request{
		AppID: "123456789", IpaPath: ipaPath, Wait: false,
	}, uploader.Hooks{})
	if err == nil {
		t.Fatal("错误 Key ID 应失败")
	}
	if !strings.Contains(err.Error(), "401") && !strings.Contains(err.Error(), "Key ID") {
		t.Errorf("错误信息应提示 401/Key ID: %v", err)
	}
}

// TestWebUIHandlerFlow 验证 GUI 本地 /api 接口的完整链路
// （与前端实际调用方式一致：start → 轮询 job → done）。
func TestWebUIHandlerFlow(t *testing.T) {
	cfg, ipaPath, _ := setup(t)
	srv := webui.New(uploader.NewJobManager())
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	post := func(path string, body any) map[string]any {
		t.Helper()
		raw, _ := json.Marshal(body)
		res, err := http.Post(ts.URL+path, "application/json", strings.NewReader(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		if res.StatusCode >= 300 {
			t.Fatalf("POST %s → %d: %v", path, res.StatusCode, out)
		}
		return out
	}

	// apps：测试连接
	appsRes := post("/api/apps", map[string]any{
		"keyPath": cfg.KeyPath, "keyId": cfg.KeyID, "issuerId": cfg.IssuerID, "apiBase": cfg.APIBase,
	})
	apps := appsRes["apps"].([]any)
	if len(apps) != 1 {
		t.Fatalf("应列出 1 个 App: %v", appsRes)
	}

	// parse-path：路径解析
	parseRes := post("/api/parse-path", map[string]any{"path": ipaPath})
	if parseRes["info"] == nil {
		t.Fatalf("应解析出包信息: %v", parseRes)
	}

	// start → 轮询 job
	post("/api/start", map[string]any{
		"keyPath": cfg.KeyPath, "keyId": cfg.KeyID, "issuerId": cfg.IssuerID, "apiBase": cfg.APIBase,
		"appId": "123456789", "ipaPath": ipaPath, "wait": true,
	})
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := http.Get(ts.URL + "/api/job")
		if err != nil {
			t.Fatal(err)
		}
		var job map[string]any
		_ = json.NewDecoder(res.Body).Decode(&job)
		res.Body.Close()
		state, _ := job["state"].(string)
		if state == "done" {
			result := job["result"].(map[string]any)
			if result["buildId"] == "" {
				t.Error("done 后应有 buildId")
			}
			if job["bytesTotal"].(float64) <= 0 {
				t.Error("bytesTotal 应大于 0")
			}
			return
		}
		if state == "failed" {
			t.Fatalf("任务失败: %v", job["error"])
		}
		if time.Now().After(deadline) {
			t.Fatalf("超时未完成，最后状态: %v", job)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestListBuildUploads 验证历史上传查询。
func TestListBuildUploads(t *testing.T) {
	cfg, ipaPath, mock := setup(t)
	_, err := uploader.Run(context.Background(), cfg, uploader.Request{
		AppID: "123456789", IpaPath: ipaPath, Wait: false,
	}, uploader.Hooks{})
	if err != nil {
		t.Fatal(err)
	}
	client, err := ascapi.NewClient(ascapi.Credentials{
		KeyPath: cfg.KeyPath, KeyID: cfg.KeyID, IssuerID: cfg.IssuerID, APIBase: cfg.APIBase,
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := ascapi.ListBuildUploads(context.Background(), client, "123456789", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("应列出 1 条上传记录，实际 %d 条", len(rows))
	}
	t.Logf("历史上传状态: %s", rows[0].State.State)
	_ = mock
}

// TestUploadIndividualKeyFlow 个人密钥全流程：认证不带 IssuerID，
// JWT payload 为 sub=user（官方：个人密钥不用 iss），mock 自适应验签。
func TestUploadIndividualKeyFlow(t *testing.T) {
	cfg, ipaPath, mock := setup(t)
	cfg.KeyType = uploader.KeyTypeIndividual
	cfg.IssuerID = "" // 个人密钥不需要 Issuer ID
	res, err := uploader.Run(context.Background(), cfg, uploader.Request{
		AppID: "123456789", IpaPath: ipaPath, Wait: false,
	}, uploader.Hooks{})
	if err != nil {
		t.Fatalf("个人密钥上传失败: %v", err)
	}
	if res.BuildUploadID == "" {
		t.Error("缺少 BuildUploadID")
	}
	if v := mock.Violations(); len(v) > 0 {
		t.Errorf("协议违规: %v", v)
	}
}

// TestTeamKeyRejectsWrongIssuer 团队密钥 IssuerID 错误应被 mock 拒绝（401）。
func TestTeamKeyRejectsWrongIssuer(t *testing.T) {
	cfg, ipaPath, _ := setup(t)
	cfg.IssuerID = "wrong-issuer" // 与 mock 期望不符
	_, err := uploader.Run(context.Background(), cfg, uploader.Request{
		AppID: "123456789", IpaPath: ipaPath, Wait: false,
	}, uploader.Hooks{})
	if err == nil {
		t.Fatal("错误 IssuerID 应失败")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("应返回 401 类错误: %v", err)
	}
}
