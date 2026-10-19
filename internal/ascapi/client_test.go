package ascapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// needKey 挂一个会读取 .p8 的客户端：这里只测 do() 的重试逻辑，
// 用非法 keyPath 会在 NewClient 失败，因此直接构造 Client。
func newTestClient(base string) *Client {
	return &Client{Base: base, tokens: &stubTokens{}, http: &http.Client{Timeout: 5 * time.Second}}
}

type stubTokens struct{}

func (s *stubTokens) Token(force bool) (string, error) { return "stub-token", nil }

func TestRetryOn500ThenSuccess(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	var out map[string]any
	if err := c.do(context.Background(), "GET", "/v1/x", nil, &out); err != nil {
		t.Fatalf("重试后应成功: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("应有 2 次调用，实际 %d", calls)
	}
}

func TestAuthRefreshOn401(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			w.WriteHeader(401)
			_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"code": "NOT_AUTHORIZED", "detail": "expired"}}})
			return
		}
		if r.Header.Get("Authorization") == "Bearer refreshed-token" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
			return
		}
		w.WriteHeader(401)
	}))
	defer srv.Close()

	tokens := &refreshableTokens{}
	c := &Client{Base: srv.URL, tokens: tokens, http: &http.Client{Timeout: 5 * time.Second}}
	var out map[string]any
	if err := c.do(context.Background(), "GET", "/v1/x", nil, &out); err != nil {
		t.Fatalf("401 重签后应成功: %v", err)
	}
	if !tokens.forced {
		t.Error("401 后应以 force=true 重签 token")
	}
}

type refreshableTokens struct {
	forced bool
}

func (s *refreshableTokens) Token(force bool) (string, error) {
	if force {
		s.forced = true
		return "refreshed-token", nil
	}
	return "stub-token", nil
}

func TestErrorShapeAndHint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{
			"id": "e1", "status": "403", "code": "FORBIDDEN", "title": "Forbidden", "detail": "no access",
		}}})
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	err := c.do(context.Background(), "GET", "/v1/x", nil, nil)
	ae, ok := err.(*Error)
	if !ok {
		t.Fatalf("应返回 *Error，实际 %T: %v", err, err)
	}
	if ae.Status != 403 || ae.Code != "FORBIDDEN" {
		t.Errorf("错误字段错误: %+v", ae)
	}
	if ae.Hint() == "" {
		t.Error("403 应有中文提示")
	}
}

func TestNoRetryOn400(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
	}))
	defer srv.Close()
	c := newTestClient(srv.URL)
	if err := c.do(context.Background(), "GET", "/v1/x", nil, nil); err == nil {
		t.Fatal("400 应报错")
	}
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatalf("400 不应重试，实际调用 %d 次", calls)
	}
}
