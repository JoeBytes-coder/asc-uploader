package jwt

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
)

func writeTestKey(t *testing.T) (p8Path string, pub *ecdsa.PublicKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(key)
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	p8Path = filepath.Join(t.TempDir(), "AuthKey_TEST.p8")
	if err := os.WriteFile(p8Path, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	return p8Path, &key.PublicKey
}

func TestTokenSignAndVerify(t *testing.T) {
	p8Path, pub := writeTestKey(t)
	tp, err := New(p8Path, "KEY123", "issuer-uuid-1")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := tp.Token(false)
	if err != nil {
		t.Fatal(err)
	}
	parts := split3(tok)
	if len(parts) != 3 {
		t.Fatalf("token 应为三段: %q", tok)
	}
	// header
	headerRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var header struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(headerRaw, &header) != nil || header.Alg != "ES256" || header.Kid != "KEY123" || header.Typ != "JWT" {
		t.Fatalf("header 错误（应含 alg/kid/typ=JWT）: %s", headerRaw)
	}
	// payload
	payloadRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss string `json:"iss"`
		Aud string `json:"aud"`
		Exp int64  `json:"exp"`
		Iat int64  `json:"iat"`
	}
	if json.Unmarshal(payloadRaw, &claims) != nil {
		t.Fatal("payload 非法")
	}
	if claims.Iss != "issuer-uuid-1" || claims.Aud != "appstoreconnect-v1" {
		t.Fatalf("claims 错误: %s", payloadRaw)
	}
	if claims.Exp-claims.Iat != 18*60 {
		t.Fatalf("有效期应为 18 分钟，实际 %d", claims.Exp-claims.Iat)
	}
	// 签名（r||s 64 字节）
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 签名应为 64 字节，实际 %d", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("ES256 签名验证失败")
	}
}

func TestTokenCacheAndForce(t *testing.T) {
	p8Path, _ := writeTestKey(t)
	tp, _ := New(p8Path, "K", "I")
	a, _ := tp.Token(false)
	b, _ := tp.Token(false)
	if a != b {
		t.Error("有效期内应复用缓存")
	}
	c, _ := tp.Token(true)
	if c == a {
		t.Error("force 应重签")
	}
}

func TestInvalidKeyFile(t *testing.T) {
	dir := t.TempDir()
	// 非 PEM 内容
	p1 := filepath.Join(dir, "bad.p8")
	_ = os.WriteFile(p1, []byte("not a pem"), 0o600)
	if _, err := New(p1, "K", "I"); err == nil {
		t.Error("非 PEM 应报错")
	}
	// PKCS8 但不是 EC P-256（Ed25519）
	_, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	der, _ := x509.MarshalPKCS8PrivateKey(edPriv)
	p2 := filepath.Join(dir, "ed.p8")
	_ = os.WriteFile(p2, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600)
	if _, err := New(p2, "K", "I"); err == nil {
		t.Error("非 EC P-256 私钥应报错")
	}
	// 文件不存在
	if _, err := New(filepath.Join(dir, "missing.p8"), "K", "I"); err == nil {
		t.Error("文件不存在应报错")
	}
}

func split3(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '.' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func TestIndividualTokenPayload(t *testing.T) {
	p8Path, pub := writeTestKey(t)
	tp, err := NewIndividual(p8Path, "INDKEY001")
	if err != nil {
		t.Fatal(err)
	}
	tok, err := tp.Token(false)
	if err != nil {
		t.Fatal(err)
	}
	parts := split3(tok)
	if len(parts) != 3 {
		t.Fatalf("token 应为三段: %q", tok)
	}
	// 官方要求 header 含 typ=JWT（个人密钥同样适用）
	headerRaw, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
		Typ string `json:"typ"`
	}
	if json.Unmarshal(headerRaw, &h) != nil || h.Typ != "JWT" || h.Kid != "INDKEY001" {
		t.Errorf("个人密钥 header 应含 typ=JWT 与 kid: %s", headerRaw)
	}
	payloadRaw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims map[string]any
	if json.Unmarshal(payloadRaw, &claims) != nil {
		t.Fatal("payload 非法")
	}
	if v, ok := claims["sub"].(string); !ok || v != "user" {
		t.Errorf("个人密钥 payload 应含 sub=user，实际: %s", payloadRaw)
	}
	if _, hasIss := claims["iss"]; hasIss {
		t.Errorf("个人密钥 payload 不应含 iss（官方：不用 Issuer ID），实际: %s", payloadRaw)
	}
	if claims["aud"] != "appstoreconnect-v1" {
		t.Errorf("aud 错误: %s", payloadRaw)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("签名应为 64 字节，实际 %d", len(sig))
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if !ecdsa.Verify(pub, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("个人密钥 ES256 签名验证失败")
	}
}

func TestNewWithTypeRejectsUnknown(t *testing.T) {
	p8Path, _ := writeTestKey(t)
	if _, err := NewWithType(p8Path, "K", "I", "bogus"); err == nil {
		t.Error("未知密钥类型应报错")
	}
}
