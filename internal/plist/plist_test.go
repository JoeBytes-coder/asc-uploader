package plist

import (
	"testing"

	"asc-uploader/internal/testutil"
)

func TestBinaryPlistRoundtrip(t *testing.T) {
	dict := map[string]any{
		"CFBundleIdentifier":         "com.example.demo",
		"CFBundleName":               "演示应用",
		"CFBundleShortVersionString": "2.0.1",
		"CFBundleVersion":            "777",
		"MinimumOSVersion":           "12.0",
		"BuildInt":                   int64(777),
		"IsRelease":                  true,
		"IsDebug":                    false,
		"Score":                      3.14,
		"Platforms":                  []any{"iPhoneOS", "XRSimulatorOS"},
		"Nested":                     map[string]any{"inner": "value"},
	}
	data := testutil.EncodeBinaryPlist(dict)
	got, err := Parse(data)
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got["CFBundleIdentifier"] != "com.example.demo" {
		t.Errorf("CFBundleIdentifier = %v", got["CFBundleIdentifier"])
	}
	if got["CFBundleName"] != "演示应用" {
		t.Errorf("UTF-16 字符串解析错误: %v", got["CFBundleName"])
	}
	if got["CFBundleShortVersionString"] != "2.0.1" || got["CFBundleVersion"] != "777" {
		t.Errorf("版本/构建号解析错误: %v / %v", got["CFBundleShortVersionString"], got["CFBundleVersion"])
	}
	if n, ok := got["BuildInt"].(int64); !ok || n != 777 {
		t.Errorf("整数解析错误: %v (%T)", got["BuildInt"], got["BuildInt"])
	}
	if got["IsRelease"] != true || got["IsDebug"] != false {
		t.Errorf("布尔解析错误: %v %v", got["IsRelease"], got["IsDebug"])
	}
	plats, ok := got["Platforms"].([]any)
	if !ok || len(plats) != 2 || plats[0] != "iPhoneOS" {
		t.Errorf("数组解析错误: %v", got["Platforms"])
	}
	nested, ok := got["Nested"].(map[string]any)
	if !ok || nested["inner"] != "value" {
		t.Errorf("嵌套字典解析错误: %v", got["Nested"])
	}
}

func TestParseXMLPlist(t *testing.T) {
	xml := "<?xml version=\"1.0\"?><plist version=\"1.0\"><dict>" +
		"<key>CFBundleIdentifier</key><string>com.example.xml</string>" +
		"<key>CFBundleVersion</key><string>42</string>" +
		"<key>CFBundleSupportedPlatforms</key><array><string>iPhoneOS</string><string>iPhoneSimulator</string></array>" +
		"</dict></plist>"
	got, err := Parse([]byte(xml))
	if err != nil {
		t.Fatalf("解析失败: %v", err)
	}
	if got["CFBundleIdentifier"] != "com.example.xml" || got["CFBundleVersion"] != "42" {
		t.Errorf("XML 键值解析错误: %v", got)
	}
	plats, ok := got["CFBundleSupportedPlatforms"].([]string)
	if !ok || len(plats) != 2 || plats[1] != "iPhoneSimulator" {
		t.Errorf("XML 数组解析错误: %v", got["CFBundleSupportedPlatforms"])
	}
}

func TestParseInvalid(t *testing.T) {
	if _, err := Parse([]byte("neither bplist nor xml")); err == nil {
		t.Error("应返回错误")
	}
}
