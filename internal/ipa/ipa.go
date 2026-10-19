// Package ipa 从 iOS/tvOS/visionOS 的 .ipa（zip）安装包中
// 读取 Payload/*.app/Info.plist，提取 Bundle ID、版本号与构建号。
// 无需第三方依赖：zip 与 plist 解析分别使用标准库与 internal/plist。
package ipa

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"asc-uploader/internal/plist"
)

var appInfoPlistRe = regexp.MustCompile(`(?i)^Payload/[^/]+\.app/Info\.plist$`)

// Info 描述安装包内解析出的应用元信息。
type Info struct {
	Name      string   `json:"name"`
	BundleID  string   `json:"bundleId"`
	Version   string   `json:"version"`
	Build     string   `json:"build"`
	MinOS     string   `json:"minOs"`
	Platforms []string `json:"platforms"`
}

// ReadInfo 打开安装包并读取 Info.plist。
//
// .ipa 可能内嵌多个 *.app/Info.plist（例如 App Clip、内嵌框架中的 decoy），
// 这里选择路径最短的一个——即 Payload 顶层的主 App。
func ReadInfo(path string) (*Info, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("不是有效的 zip/ipa/pkg 文件: %w", err)
	}
	defer zr.Close()
	var candidates []*zip.File
	for _, f := range zr.File {
		if appInfoPlistRe.MatchString(f.Name) {
			candidates = append(candidates, f)
		}
	}
	if len(candidates) == 0 {
		return nil, errors.New("包内找不到 Payload/*.app/Info.plist，请确认这是有效的 .ipa 安装包（.pkg 包内无 Info.plist，请手动填版本号）")
	}
	sort.Slice(candidates, func(i, j int) bool { return len(candidates[i].Name) < len(candidates[j].Name) })
	rc, err := candidates[0].Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, 32<<20))
	if err != nil {
		return nil, err
	}
	dict, err := plist.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("解析 Info.plist 失败: %w", err)
	}
	info := &Info{}
	info.Name, _ = dict["CFBundleName"].(string)
	if info.Name == "" {
		info.Name, _ = dict["CFBundleDisplayName"].(string)
	}
	info.BundleID, _ = dict["CFBundleIdentifier"].(string)
	info.Version, _ = dict["CFBundleShortVersionString"].(string)
	info.Build, _ = dict["CFBundleVersion"].(string)
	info.MinOS, _ = dict["MinimumOSVersion"].(string)
	if plats, ok := dict["CFBundleSupportedPlatforms"].([]any); ok {
		for _, p := range plats {
			if s, ok := p.(string); ok {
				info.Platforms = append(info.Platforms, s)
			}
		}
	}
	return info, nil
}

// UTI 返回安装包文件对应的 Apple UTI 标识（上传接口必填）。
func UTI(fileName string) (string, error) {
	switch strings.ToLower(filepath.Ext(fileName)) {
	case ".ipa":
		return "com.apple.ipa", nil
	case ".pkg":
		return "com.apple.pkg", nil
	}
	return "", fmt.Errorf("仅支持 .ipa（iOS/tvOS/visionOS）或 .pkg（macOS）文件")
}
