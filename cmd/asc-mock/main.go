// asc-mock 在本机启动一个模拟 App Store Connect API 的服务器，
// 配合 asc-uploader-cli / asc-uploader-ui 做离线端到端演示与联调：
//
//	# 1) 生成测试密钥与测试安装包（一次性）
//	asc-mock.exe --generate-keys testdata
//	# 2) 启动模拟服务器（小分片，便于观察并发上传）
//	asc-mock.exe --pubkey testdata/pubkey.pem --issuer mock-issuer --keyid MOCKKEY001 --part 65536
//	# 3) 用 CLI 对着 mock 走完整流程
//	asc-uploader-cli.exe upload --key-file testdata/AuthKey_MOCKKEY001.p8 --key-id MOCKKEY001 ^
//	    --issuer-id mock-issuer --app-id 123456789 --ipa testdata/Demo.ipa --api-base http://127.0.0.1:PORT
package main

import (
	"crypto/ecdsa"
	"crypto/x509"
	"encoding/pem"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"asc-uploader/internal/mockasc"
	"asc-uploader/internal/testutil"
)

func main() {
	genDir := flag.String("generate-keys", "", "生成测试密钥（.p8 + pubkey.pem）与测试 Demo.ipa 到该目录后退出")
	pubkeyPath := flag.String("pubkey", "", "JWT 验签公钥 PEM（配合 --generate-keys 产物使用）")
	issuer := flag.String("issuer", "mock-issuer", "期望的 Issuer ID")
	keyID := flag.String("keyid", "MOCKKEY001", "期望的 Key ID")
	partSize := flag.Int64("part", 1<<20, "分片大小（字节）")
	polls := flag.Int("process-after-polls", 2, "提交后第 N 次状态查询转为 COMPLETE")
	flag.Parse()

	if *genDir != "" {
		if err := os.MkdirAll(*genDir, 0o755); err != nil {
			fatal(err)
		}
		key, err := testutil.NewAPIKey(*genDir, *keyID, *issuer)
		if err != nil {
			fatal(err)
		}
		pubDER, err := x509.MarshalPKIXPublicKey(&key.PrivateKey.PublicKey)
		if err != nil {
			fatal(err)
		}
		pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
		if err := os.WriteFile(filepath.Join(*genDir, "pubkey.pem"), pubPEM, 0o644); err != nil {
			fatal(err)
		}
		ipa, err := testutil.BuildIPA(*genDir, map[string]any{
			"CFBundleIdentifier":         "com.example.demo",
			"CFBundleName":               "Mock 演示应用",
			"CFBundleShortVersionString": "2.0.1",
			"CFBundleVersion":            "777",
			"CFBundleSupportedPlatforms": []any{"iPhoneOS"},
		})
		if err != nil {
			fatal(err)
		}
		fmt.Printf("已生成:\n  私钥: %s\n  公钥: %s\n  安装包: %s\n", key.P8Path, filepath.Join(*genDir, "pubkey.pem"), ipa)
		return
	}

	if *pubkeyPath == "" {
		fatal(fmt.Errorf("需要 --pubkey <公钥PEM>（可先用 --generate-keys 生成）"))
	}
	raw, err := os.ReadFile(*pubkeyPath)
	if err != nil {
		fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		fatal(fmt.Errorf("公钥不是有效 PEM"))
	}
	pubAny, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		fatal(err)
	}
	pub, ok := pubAny.(*ecdsa.PublicKey)
	if !ok {
		fatal(fmt.Errorf("公钥不是 EC P-256"))
	}
	srv, err := mockasc.Start(mockasc.Options{
		PublicKey:         pub,
		IssuerID:          *issuer,
		KeyID:             *keyID,
		PartSize:          *partSize,
		ProcessAfterPolls: *polls,
	})
	if err != nil {
		fatal(err)
	}
	fmt.Printf("mock server: %s\n", srv.URL)
	select {}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "asc-mock:", err)
	os.Exit(1)
}
