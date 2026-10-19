// Package ascapi 封装 App Store Connect API 中与构建上传相关的端点：
//
//	POST   /v1/buildUploads              创建上传会话
//	POST   /v1/buildUploadFiles          预留文件位，获取分片上传操作
//	PATCH  /v1/buildUploadFiles/{id}     提交文件（附 MD5 校验和）
//	GET    /v1/buildUploads/{id}         查询处理状态（?include=build）
//	GET    /v1/apps                      列出可访问的 App
//	GET    /v1/apps/{id}/buildUploads    列出某 App 的历史上传
//
// 客户端内置：ES256 JWT 自动签发与过期刷新、401 自动重签、
// 429/5xx 指数退避重试、Apple 错误结构解析与中文排查提示。
package ascapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"asc-uploader/internal/jwt"
)

// DefaultBase 是 App Store Connect API 官方地址。
const DefaultBase = "https://api.appstoreconnect.apple.com"

// Credentials 是一份 API 密钥凭据。
type Credentials struct {
	KeyPath  string `json:"keyPath"`  // .p8 私钥文件路径
	KeyID    string `json:"keyId"`    // Key ID（AuthKey_ 文件名后半段）
	IssuerID string `json:"issuerId"` // Issuer ID（团队密钥必填；个人密钥不使用——payload 用 sub="user" 代替）
	APIBase  string `json:"apiBase"`  // 覆盖 API 地址（留空用官方；测试用）
	KeyType  string `json:"keyType"`  // team（默认）/ individual；决定 JWT payload 结构
}

// Error 表示 Apple 返回的业务错误，携带中文排查提示。
type Error struct {
	Status int
	Code   string
	Detail string
}

func (e *Error) Error() string { return fmt.Sprintf("HTTP %d %s: %s", e.Status, e.Code, e.Detail) }

// Hint 针对常见错误码给出中文排查建议。
func (e *Error) Hint() string {
	text := strings.ToLower(e.Code + " " + e.Detail)
	switch {
	case e.Status == 401:
		return "Token 无效或已过期：请检查私钥文件与 Key ID 是否匹配（Key ID 必须对应该 .p8 文件）；团队密钥还需正确的 Issuer ID（Team Keys 页顶部 UUID），个人密钥则不需要 Issuer ID（认证用 sub=user）——请核对密钥类型设置。另请检查系统时间是否正确。"
	case e.Status == 403:
		return "API 密钥权限不足：请确认密钥角色至少为 App Manager（应用管理员），且该 App 在密钥可访问范围内。"
	case e.Status == 404:
		return "资源不存在：请检查 App ID 是否正确。"
	case e.Status == 429:
		return "请求被限流，将自动退避重试。"
	case strings.Contains(text, "already") || strings.Contains(text, "duplicate"):
		return "该版本号 + 构建号可能已存在：请递增构建号（CFBundleVersion）后重新打包上传。"
	}
	return ""
}

// WithHint 返回附带了中文提示的错误文本（用于直接展示）。
func (e *Error) WithHint() string {
	if h := e.Hint(); h != "" {
		return e.Error() + "（" + h + "）"
	}
	return e.Error()
}

// tokenSource 抽象 JWT 签发（便于测试注入）。
type tokenSource interface {
	Token(force bool) (string, error)
}

// Client 是 API 客户端，并发安全。
type Client struct {
	Base   string
	tokens tokenSource
	http   *http.Client
}

// NewClient 用密钥凭据构造客户端（解析并缓存私钥）。
//
// 团队密钥要求 IssuerID 非空；个人密钥忽略 IssuerID（JWT payload 用 sub="user"）。
func NewClient(cred Credentials) (*Client, error) {
	keyType := cred.KeyType
	if keyType == "" {
		keyType = jwt.KeyTypeTeam
	}
	if keyType == jwt.KeyTypeTeam && cred.IssuerID == "" {
		return nil, fmt.Errorf("团队密钥必须提供 Issuer ID（Team Keys 页顶部的 UUID）；如使用个人密钥请设置 keyType=individual")
	}
	var tp *jwt.TokenProvider
	var err error
	if keyType == jwt.KeyTypeIndividual {
		tp, err = jwt.NewIndividual(cred.KeyPath, cred.KeyID)
	} else {
		tp, err = jwt.New(cred.KeyPath, cred.KeyID, cred.IssuerID)
	}
	if err != nil {
		return nil, err
	}
	base := cred.APIBase
	if base == "" {
		base = DefaultBase
	}
	return &Client{Base: strings.TrimRight(base, "/"), tokens: tp, http: &http.Client{Timeout: 90 * time.Second}}, nil
}

// do 执行一次 API 请求（带 401 重签与 429/5xx 指数退避，最多 4 次）。
func (c *Client) do(ctx context.Context, method, pth string, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	full := c.Base + pth
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<(attempt-1)) * time.Second):
			}
		}
		force := false
		if ae, ok := lastErr.(*Error); ok && ae.Status == 401 {
			force = true
		}
		tok, err := c.tokens.Token(force)
		if err != nil {
			return err
		}
		var reqBody io.Reader
		if payload != nil {
			reqBody = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(ctx, method, full, reqBody)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Accept", "application/json")
		if payload != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		res, err := c.http.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(res.Body, 8<<20))
		res.Body.Close()
		if readErr != nil {
			lastErr = readErr
			continue // 读响应体失败按网络错误退避重试
		}
		if res.StatusCode == 401 && attempt == 0 {
			lastErr = &Error{Status: 401, Detail: "未授权"}
			continue
		}
		if res.StatusCode == 429 || res.StatusCode >= 500 {
			if attempt < 3 {
				lastErr = &Error{Status: res.StatusCode, Detail: strings.TrimSpace(string(data))}
				continue
			}
		}
		if res.StatusCode >= 400 {
			return parseErrorBody(res.StatusCode, data)
		}
		if out != nil {
			return json.Unmarshal(data, out)
		}
		return nil
	}
	return lastErr
}

// parseErrorBody 把 Apple 的 JSON:API 错误结构转换为 *Error。
func parseErrorBody(status int, data []byte) error {
	var er struct {
		Errors []struct {
			Status string `json:"status"`
			Code   string `json:"code"`
			Title  string `json:"title"`
			Detail string `json:"detail"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(data, &er)
	if len(er.Errors) > 0 {
		e0 := er.Errors[0]
		detail := e0.Detail
		if detail == "" {
			detail = e0.Title
		}
		return &Error{Status: status, Code: e0.Code, Detail: detail}
	}
	return &Error{Status: status, Detail: strings.TrimSpace(string(data))}
}
