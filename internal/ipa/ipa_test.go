package ipa

import (
	"os"
	"path/filepath"
	"testing"

	"asc-uploader/internal/testutil"
)

func TestReadInfoPicksShortestAppPath(t *testing.T) {
	dir := t.TempDir()
	ipaPath, err := testutil.BuildIPA(dir, map[string]any{
		"CFBundleIdentifier":         "com.example.main",
		"CFBundleName":               "主应用",
		"CFBundleShortVersionString": "2.0.1",
		"CFBundleVersion":            "777",
		"MinimumOSVersion":           "12.0",
		"CFBundleSupportedPlatforms": []any{"iPhoneOS"},
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := ReadInfo(ipaPath)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	// BuildIPA 内含一个嵌套 decoy（com.example.inner-decoy），必须选中主 App
	if info.BundleID != "com.example.main" {
		t.Errorf("应选择最短路径的主 App，实际 BundleID=%s", info.BundleID)
	}
	if info.Version != "2.0.1" || info.Build != "777" {
		t.Errorf("版本/构建号错误: %s/%s", info.Version, info.Build)
	}
	if info.Name != "主应用" {
		t.Errorf("应用名错误: %s", info.Name)
	}
	if len(info.Platforms) != 1 || info.Platforms[0] != "iPhoneOS" {
		t.Errorf("平台错误: %v", info.Platforms)
	}
}

func TestReadInfoRejectsNonIPA(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "plain.zip")
	_ = os.WriteFile(p, []byte("not a zip"), 0o644)
	if _, err := ReadInfo(p); err == nil {
		t.Error("非 zip 应报错")
	}
}

func TestUTI(t *testing.T) {
	for name, want := range map[string]string{
		"app.ipa": "com.apple.ipa", "mac.pkg": "com.apple.pkg",
	} {
		got, err := UTI(name)
		if err != nil || got != want {
			t.Errorf("UTI(%s) = %v, %v；期望 %s", name, got, err, want)
		}
	}
	if _, err := UTI("app.apk"); err == nil {
		t.Error("不支持的后缀应报错")
	}
}
