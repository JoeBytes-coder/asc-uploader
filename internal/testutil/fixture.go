// Package testutil 提供测试夹具构造工具：
//
//   - EncodeBinaryPlist：极简二进制 plist（bplist00）编码器，
//     支持 bool/int64/float64/string/[]any/map[string]any，
//     与 internal/plist 的解析器形成往返（roundtrip）验证
//   - BuildIPA：构造带 decoy 嵌套 .app 的测试安装包
//   - NewAPIKey：生成 EC P-256 密钥并写成 .p8（PKCS#8 PEM）
package testutil

import (
	"archive/zip"
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

/* ---------------- 二进制 plist 编码器（测试专用） ---------------- */

type plObject struct {
	marker   byte   // 完整 marker（含 count 低 4 位；0x0F 表示扩展长度）
	extCount []byte // 扩展长度（info=0xF 时：0x10|log2size + 大端长度）
	payload  []byte // 标量负载
	children []int  // 数组/字典子对象索引（字典为先 key 后 value）
	isDict   bool
}

type plEncoder struct {
	objs []plObject
}

// EncodeBinaryPlist 把 dict 编码为 bplist00 字节。
func EncodeBinaryPlist(dict map[string]any) []byte {
	e := &plEncoder{}
	top := e.build(dict)
	refSize := sizeFor(len(e.objs))
	bodies := make([][]byte, len(e.objs))
	for i := range e.objs {
		bodies[i] = e.render(&e.objs[i], refSize)
	}
	offsets := make([]int, len(bodies))
	off := 8 // "bplist00"
	for i, b := range bodies {
		offsets[i] = off
		off += len(b)
	}
	// 偏移表紧随对象数据；offsetSize 只需覆盖最大偏移值（表起始位置）
	offsetSize := sizeFor(off)
	buf := bytes.NewBufferString("bplist00")
	for _, b := range bodies {
		buf.Write(b)
	}
	tableOff := buf.Len()
	for _, o := range offsets {
		writeNBE(buf, uint64(o), offsetSize)
	}
	// trailer 32 字节
	trailer := make([]byte, 32)
	trailer[6] = byte(offsetSize)
	trailer[7] = byte(refSize)
	binary.BigEndian.PutUint64(trailer[8:], uint64(len(e.objs)))
	binary.BigEndian.PutUint64(trailer[16:], uint64(top))
	binary.BigEndian.PutUint64(trailer[24:], uint64(tableOff))
	buf.Write(trailer)
	return buf.Bytes()
}

func (e *plEncoder) render(o *plObject, refSize int) []byte {
	out := []byte{o.marker}
	if o.extCount != nil {
		out = append(out, o.extCount...)
	}
	out = append(out, o.payload...)
	for _, c := range o.children {
		out = appendNBE(out, uint64(c), refSize)
	}
	return out
}

func appendNBE(dst []byte, v uint64, n int) []byte {
	for i := n - 1; i >= 0; i-- {
		dst = append(dst, byte(v>>(8*uint(i))))
	}
	return dst
}

func writeNBE(buf *bytes.Buffer, v uint64, n int) {
	for i := n - 1; i >= 0; i-- {
		buf.WriteByte(byte(v >> (8 * uint(i))))
	}
}

// sizeFor 返回表示 v 所需字节数（1/2/4/8）。
func sizeFor(v int) int {
	switch {
	case v < 1<<8:
		return 1
	case v < 1<<16:
		return 2
	case v < 1<<32:
		return 4
	default:
		return 8
	}
}

// build 递归展开对象树并登记，返回对象索引。
func (e *plEncoder) build(v any) int {
	switch t := v.(type) {
	case nil:
		return e.push(0x00, nil, nil, nil, false)
	case bool:
		if t {
			return e.push(0x09, nil, nil, nil, false)
		}
		return e.push(0x08, nil, nil, nil, false)
	case int:
		return e.build(int64(t))
	case int64:
		return e.pushInt(t)
	case float64:
		var p [8]byte
		binary.BigEndian.PutUint64(p[:], math.Float64bits(t))
		return e.push(0x23, nil, p[:], nil, false)
	case string:
		return e.pushString(t)
	case []any:
		idx := len(e.objs)
		e.objs = append(e.objs, plObject{}) // 占位，稍后回填 children
		var children []int
		for _, item := range t {
			children = append(children, e.build(item))
		}
		e.objs[idx] = e.container(0xA0, len(t), children, false)
		return idx
	case map[string]any:
		idx := len(e.objs)
		e.objs = append(e.objs, plObject{})
		// 同一次遍历收集键值对：bplist 字典为先全部 key 引用、后全部 value 引用
		type kv struct {
			k string
			v any
		}
		pairs := make([]kv, 0, len(t))
		for k, v := range t {
			pairs = append(pairs, kv{k, v})
		}
		var children []int
		for _, p := range pairs {
			children = append(children, e.build(p.k))
		}
		for _, p := range pairs {
			children = append(children, e.build(p.v))
		}
		e.objs[idx] = e.container(0xD0, len(pairs), children, true)
		return idx
	default:
		panic(fmt.Sprintf("testutil: 不支持的类型 %T", v))
	}
}

func (e *plEncoder) push(marker byte, ext, payload []byte, children []int, isDict bool) int {
	e.objs = append(e.objs, plObject{marker: marker, extCount: ext, payload: payload, children: children, isDict: isDict})
	return len(e.objs) - 1
}

func (e *plEncoder) pushInt(v int64) int {
	switch {
	case v >= -128 && v <= 127:
		return e.push(0x10, nil, []byte{byte(v)}, nil, false)
	case v >= -32768 && v <= 32767:
		var p [2]byte
		binary.BigEndian.PutUint16(p[:], uint16(v))
		return e.push(0x11, nil, p[:], nil, false)
	case v >= int64(-2147483648) && v <= 2147483647:
		var p [4]byte
		binary.BigEndian.PutUint32(p[:], uint32(v))
		return e.push(0x12, nil, p[:], nil, false)
	default:
		var p [8]byte
		binary.BigEndian.PutUint64(p[:], uint64(v))
		return e.push(0x13, nil, p[:], nil, false)
	}
}

func (e *plEncoder) pushString(s string) int {
	ascii := true
	for _, r := range s {
		if r > 0x7f {
			ascii = false
			break
		}
	}
	if ascii {
		payload := []byte(s)
		marker, ext := countMarker(0x50, len(payload))
		return e.push(marker, ext, payload, nil, false)
	}
	runes := []rune(s)
	payload := make([]byte, len(runes)*2)
	for i, r := range runes {
		binary.BigEndian.PutUint16(payload[i*2:], uint16(r))
	}
	marker, ext := countMarker(0x60, len(runes))
	return e.push(marker, ext, payload, nil, false)
}

// container 生成容器对象；count 是元素个数（字典为条目数，非子引用数），
// count≤14 直接写进 marker 低 4 位，更大计数用 0x?F 扩展长度。
func (e *plEncoder) container(base byte, count int, children []int, isDict bool) plObject {
	marker, ext := countMarker(base, count)
	return plObject{marker: marker, extCount: ext, children: children, isDict: isDict}
}

func countMarker(base byte, count int) (byte, []byte) {
	if count < 15 {
		return base | byte(count), nil
	}
	// 扩展长度：0x1F? 官方格式为 marker|0xF 后跟 0x10|log2(size) 再跟大端长度
	sizeBytes := sizeFor(count)
	if sizeBytes == 8 {
		sizeBytes = 8
	}
	log2 := 0
	for s := sizeBytes; s > 1; s /= 2 {
		log2++
	}
	ext := []byte{byte(0x10 | log2)}
	ext = appendNBE(ext, uint64(count), sizeBytes)
	return base | 0x0F, ext
}

/* ---------------- IPA 构造器 ---------------- */

// BuildIPA 在 dir 下生成一个测试 .ipa，返回文件路径。
//
// 包内含主 App 的二进制 plist Info.plist，以及一个嵌套在 Frameworks 里的
// decoy .app（路径更长，用于验证「取最短路径」的解析策略）。
func BuildIPA(dir string, info map[string]any) (string, error) {
	mainPlist := EncodeBinaryPlist(info)
	decoy := EncodeBinaryPlist(map[string]any{
		"CFBundleIdentifier": "com.example.inner-decoy",
		"CFBundleName":       "Decoy",
	})
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	must := func(name string, data []byte, method uint16) {
		hdr := &zip.FileHeader{Name: name, Method: method}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write(data); err != nil {
			panic(err)
		}
	}
	must("Payload/Demo.app/Info.plist", mainPlist, zip.Store)
	must("Payload/Demo.app/_CodeSignature/CodeResources", []byte("<plist/>"), zip.Deflate)
	must("Payload/Demo.app/Frameworks/Sky.framework/Resources/Inner.app/Info.plist", decoy, zip.Store)
	if err := zw.Close(); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "Demo.ipa")
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

/* ---------------- API 密钥生成 ---------------- */

// APIKey 是一对测试密钥。
type APIKey struct {
	PrivateKey *ecdsa.PrivateKey
	P8Path     string // .p8 私钥文件路径
	KeyID      string
	IssuerID   string
}

// NewAPIKey 生成 EC P-256 密钥并写出 .p8 文件（PKCS#8 PEM）。
func NewAPIKey(dir, keyID, issuerID string) (*APIKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, err
	}
	p8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	p8Path := filepath.Join(dir, "AuthKey_"+keyID+".p8")
	if err := os.WriteFile(p8Path, p8, 0o600); err != nil {
		return nil, err
	}
	return &APIKey{PrivateKey: key, P8Path: p8Path, KeyID: keyID, IssuerID: issuerID}, nil
}
