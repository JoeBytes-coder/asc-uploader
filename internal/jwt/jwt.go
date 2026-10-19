// Package jwt 实现 App Store Connect API 的 ES256 JWT 签发与缓存。
//
// Apple 要求每个 API 请求携带 Bearer Token（ES256 签名的 JWT）。
// 两种密钥的 payload 结构不同（官方文档 Generating Tokens for API Requests）：
//
//	团队密钥：{"iss":"<Issuer ID>","iat":N,"exp":N,"aud":"appstoreconnect-v1"}
//	个人密钥：{"sub":"user","iat":N,"exp":N,"aud":"appstoreconnect-v1"}
//	          （"Individual keys don't use the Issuer ID key iss,
//	            but do require the Subject key sub."）
//
//	header = {"alg":"ES256","kid":"<Key ID>"}（两种密钥相同）
//	签名   = ECDSA-P256(SHA256(signingInput)) 的 r||s 原始 64 字节（非 DER）
//
// 私钥即 App Store Connect 后台下载的 .p8 文件（PKCS#8 PEM 编码的 EC P-256 私钥）。
package jwt

import (
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"sync"
	"time"
)

// 密钥类型（决定 JWT payload 结构）。
const (
	KeyTypeTeam       = "team"       // 团队密钥：payload 含 iss（Issuer ID）
	KeyTypeIndividual = "individual" // 个人密钥：payload 含 sub="user"，无 iss
)

// subjectIndividual 是个人密钥 payload 的 sub 固定值（官方规定恒为 "user"）。
const subjectIndividual = "user"

// MaxTTL 是 Apple 允许的 Token 最长有效期（20 分钟）。
const MaxTTL = 20 * 60

// tokenTTL 是实际签发使用的有效期，留出余量避免边界过期。
const tokenTTL = 18 * 60

// TokenProvider 负责 JWT 的签发与缓存，并发安全。
type TokenProvider struct {
	key      *ecdsa.PrivateKey
	keyID    string
	issuerID string // 团队密钥使用；个人密钥忽略
	keyType  string // KeyTypeTeam（默认）/ KeyTypeIndividual

	mu     sync.Mutex
	cached string
	exp    int64
}

// New 从 .p8 私钥文件构造团队密钥 TokenProvider（payload 含 iss）。
func New(keyPath, keyID, issuerID string) (*TokenProvider, error) {
	return NewWithType(keyPath, keyID, issuerID, KeyTypeTeam)
}

// NewIndividual 从 .p8 私钥文件构造个人密钥 TokenProvider（payload 含 sub，无需 Issuer ID）。
func NewIndividual(keyPath, keyID string) (*TokenProvider, error) {
	return NewWithType(keyPath, keyID, "", KeyTypeIndividual)
}

// NewWithType 从 .p8 私钥文件构造指定类型的 TokenProvider。
func NewWithType(keyPath, keyID, issuerID, keyType string) (*TokenProvider, error) {
	raw, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("无法读取私钥文件 %s: %w", keyPath, err)
	}
	return newFromBytes(raw, keyID, issuerID, keyType)
}

// NewFromBytes 从 PEM 字节构造团队密钥 TokenProvider（测试与内存密钥场景）。
func NewFromBytes(pemBytes []byte, keyID, issuerID string) (*TokenProvider, error) {
	return newFromBytes(pemBytes, keyID, issuerID, KeyTypeTeam)
}

func newFromBytes(pemBytes []byte, keyID, issuerID, keyType string) (*TokenProvider, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, fmt.Errorf("私钥文件不是有效的 PEM 格式（应为 App Store Connect 的 .p8 文件，以 -----BEGIN PRIVATE KEY----- 开头）")
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("私钥解析失败: %w", err)
	}
	key, ok := keyAny.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		return nil, fmt.Errorf("私钥不是 EC P-256 类型（.p8 应为 App Store Connect API 密钥）")
	}
	if keyType == "" {
		keyType = KeyTypeTeam
	}
	if keyType != KeyTypeTeam && keyType != KeyTypeIndividual {
		return nil, fmt.Errorf("未知密钥类型 %q（可用: %s / %s）", keyType, KeyTypeTeam, KeyTypeIndividual)
	}
	return &TokenProvider{key: key, keyID: keyID, issuerID: issuerID, keyType: keyType}, nil
}

// Token 返回有效 Token；缓存过期前直接复用，force 为 true 时强制重签
// （用于服务端返回 401 后的自动刷新）。
func (p *TokenProvider) Token(force bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now().Unix()
	if !force && p.cached != "" && now < p.exp-120 {
		return p.cached, nil
	}
	// 官方文档要求 header 三个字段：alg、kid、typ（"JWT"）
	header := fmt.Sprintf(`{"alg":"ES256","kid":%q,"typ":"JWT"}`, p.keyID)
	var payload string
	if p.keyType == KeyTypeIndividual {
		// 个人密钥：不使用 iss（Issuer ID），固定 sub="user"
		payload = fmt.Sprintf(`{"aud":"appstoreconnect-v1","exp":%d,"iat":%d,"sub":%q}`, now+tokenTTL, now, subjectIndividual)
	} else {
		payload = fmt.Sprintf(`{"aud":"appstoreconnect-v1","exp":%d,"iat":%d,"iss":%q}`, now+tokenTTL, now, p.issuerID)
	}
	b64 := base64.RawURLEncoding
	signing := b64.EncodeToString([]byte(header)) + "." + b64.EncodeToString([]byte(payload))
	sum := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, p.key, sum[:])
	if err != nil {
		return "", fmt.Errorf("签名失败: %w", err)
	}
	// JWT 要求 ES256 签名为 r||s 各 32 字节的原始格式（非 ASN.1 DER）
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	tok := signing + "." + b64.EncodeToString(sig)
	p.cached = tok
	p.exp = now + tokenTTL
	return tok, nil
}
