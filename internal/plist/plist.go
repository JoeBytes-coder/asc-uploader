// Package plist 实现一个零依赖的 Apple plist 解析器，
// 支持 Info.plist 实际会出现的两种编码：二进制 bplist00 与 XML。
//
// 只需读出字典顶层键值（CFBundleIdentifier 等），因此解析结果为
// map[string]any；支持的数据类型覆盖 null/bool/int/float/date/
// ASCII 与 UTF-16 字符串/UID/数组/字典，不支持的类型返回错误。
package plist

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Parse 解析 plist 字节并返回顶层字典。
func Parse(data []byte) (map[string]any, error) {
	if bytes.HasPrefix(data, []byte("bplist0")) {
		v, err := parseBinary(data)
		if err != nil {
			return nil, err
		}
		if m, ok := v.(map[string]any); ok {
			return m, nil
		}
		return nil, errors.New("plist 顶层不是字典")
	}
	if strings.Contains(string(data), "<plist") {
		return parseXML(string(data)), nil
	}
	return nil, errors.New("无法识别的 plist 数据：既不是二进制（bplist00）也不是 XML 格式")
}

/* ---------------- 二进制 plist（bplist00） ---------------- */

func uintNBE(b []byte, n int) uint64 {
	var v uint64
	for i := 0; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v
}

type bplist struct {
	buf         []byte
	offsetSize  int
	refSize     int
	numObjects  int
	offsetTable int
}

func (b *bplist) offset(i int) int {
	return int(uintNBE(b.buf[b.offsetTable+i*b.offsetSize:], b.offsetSize))
}

// readCount 读取紧跟 marker 的元素个数（0x0F 表示后续为扩展长度）。
func (b *bplist) readCount(off int, info byte) (int, int, error) {
	if info != 0x0f {
		return int(info), off + 1, nil
	}
	next := b.buf[off+1]
	if next>>4 != 0x1 {
		return 0, 0, errors.New("plist 扩展长度标记无效")
	}
	size := 1 << (next & 0x0f)
	return int(uintNBE(b.buf[off+2:], size)), off + 2 + size, nil
}

func (b *bplist) readObject(off int, depth int) (any, error) {
	if depth > 64 {
		return nil, errors.New("plist 嵌套过深")
	}
	if off >= len(b.buf) {
		return nil, errors.New("plist 偏移越界")
	}
	marker := b.buf[off]
	typ := marker >> 4
	info := marker & 0x0f
	switch typ {
	case 0x0: // 单值
		switch info {
		case 0x00, 0x0f:
			return nil, nil
		case 0x08:
			return false, nil
		case 0x09:
			return true, nil
		}
		return nil, fmt.Errorf("未知 plist 标记 0x%x", marker)
	case 0x1: // 整数，长度 2^info 字节
		size := 1 << info
		if size == 16 {
			// 128 位整型极罕见，取低 64 位即可
			return int64(uintNBE(b.buf[off+9:], 8)), nil
		}
		return int64(uintNBE(b.buf[off+1:], size)), nil
	case 0x2: // 实数
		if info == 0x2 {
			return math.Float32frombits(uint32(binary.BigEndian.Uint32(b.buf[off+1:]))), nil
		}
		if info == 0x3 {
			return math.Float64frombits(binary.BigEndian.Uint64(b.buf[off+1:])), nil
		}
		return nil, errors.New("未知实数宽度")
	case 0x3: // 日期（Core Data 纪元 2001-01-01）
		t := math.Float64frombits(binary.BigEndian.Uint64(b.buf[off+1:])) + 978307200
		return time.Unix(int64(t), 0).UTC(), nil
	case 0x5: // ASCII 字符串
		ln, head, err := b.readCount(off, info)
		if err != nil {
			return nil, err
		}
		return string(b.buf[head : head+ln]), nil
	case 0x6: // UTF-16BE 字符串
		ln, head, err := b.readCount(off, info)
		if err != nil {
			return nil, err
		}
		raw := b.buf[head : head+ln*2]
		u16 := make([]rune, 0, ln)
		for i := 0; i < ln; i++ {
			u16 = append(u16, rune(binary.BigEndian.Uint16(raw[i*2:])))
		}
		return string(u16), nil
	case 0x8: // UID（keyed archiver 用）
		return int64(uintNBE(b.buf[off+1:], int(info)+1)), nil
	case 0xa, 0xb: // 数组
		count, head, err := b.readCount(off, info)
		if err != nil {
			return nil, err
		}
		out := make([]any, 0, count)
		for i := 0; i < count; i++ {
			ref := int(uintNBE(b.buf[head+i*b.refSize:], b.refSize))
			v, err := b.readObject(b.offset(ref), depth+1)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case 0xc, 0xd: // 字典：0xC0 与 0xD0 均为先 key 引用块、后 value 引用块
		count, head, err := b.readCount(off, info)
		if err != nil {
			return nil, err
		}
		out := map[string]any{}
		for i := 0; i < count; i++ {
			kref := int(uintNBE(b.buf[head+i*b.refSize:], b.refSize))
			vref := int(uintNBE(b.buf[head+(count+i)*b.refSize:], b.refSize))
			k, err := b.readObject(b.offset(kref), depth+1)
			if err != nil {
				return nil, err
			}
			v, err := b.readObject(b.offset(vref), depth+1)
			if err != nil {
				return nil, err
			}
			ks, _ := k.(string)
			out[ks] = v
		}
		return out, nil
	default:
		return nil, fmt.Errorf("暂不支持的 plist 数据类型 0x%x", marker)
	}
}

func parseBinary(buf []byte) (any, error) {
	if len(buf) < 40 {
		return nil, errors.New("bplist 太短")
	}
	t := len(buf) - 32
	b := &bplist{
		buf:         buf,
		offsetSize:  int(buf[t+6]),
		refSize:     int(buf[t+7]),
		numObjects:  int(binary.BigEndian.Uint64(buf[t+8:])),
		offsetTable: int(binary.BigEndian.Uint64(buf[t+24:])),
	}
	top := int(binary.BigEndian.Uint64(buf[t+16:]))
	if b.offsetSize < 1 || b.refSize < 1 || top >= b.numObjects {
		return nil, errors.New("bplist 头部非法")
	}
	return b.readObject(b.offset(top), 0)
}

/* ---------------- XML plist（轻量正则提取） ---------------- */

var xmlPairRe = regexp.MustCompile(`(?s)<key>([^<]+)</key>\s*<(string|integer|true|false)\b[^>]*>([^<]*)<`)
var xmlPlatRe = regexp.MustCompile(`(?s)<key>CFBundleSupportedPlatforms</key>\s*<array>(.*?)</array>`)
var xmlStrRe = regexp.MustCompile(`<string>([^<]*)</string>`)

// parseXML 只提取本工具关心的扁平键值与 CFBundleSupportedPlatforms 数组。
func parseXML(text string) map[string]any {
	out := map[string]any{}
	for _, m := range xmlPairRe.FindAllStringSubmatch(text, -1) {
		key, tag, val := m[1], m[2], strings.TrimSpace(m[3])
		switch tag {
		case "string":
			out[key] = val
		case "integer":
			n, _ := strconv.ParseInt(val, 10, 64)
			out[key] = n
		case "true", "false":
			out[key] = tag == "true"
		}
	}
	if m := xmlPlatRe.FindStringSubmatch(text); m != nil {
		plats := []string{}
		for _, s := range xmlStrRe.FindAllStringSubmatch(m[1], -1) {
			plats = append(plats, strings.TrimSpace(s[1]))
		}
		out["CFBundleSupportedPlatforms"] = plats
	}
	return out
}
