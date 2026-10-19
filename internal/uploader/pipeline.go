// Package uploader 实现「IPA → App Store Connect」的完整上传编排，
// 官方五步流程 + 状态轮询共六个阶段：
//
//	prepare  解析安装包（版本号 / 构建号自动读取）
//	session  POST /v1/buildUploads          创建上传会话
//	reserve  POST /v1/buildUploadFiles      预留文件位，获取分片操作
//	upload   按 uploadOperations 并发上传字节
//	commit   PATCH /v1/buildUploadFiles/{id} 提交文件（附 MD5）
//	wait     GET  /v1/buildUploads/{id}     轮询直至 COMPLETE / FAILED
//
// CLI 与 GUI 共用这一条流水线：Run 同步执行并通过 Hooks 上报进度。
package uploader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"asc-uploader/internal/ascapi"
	"asc-uploader/internal/ipa"
)

// 阶段名（GUI 步骤条与日志按此展示）。
const (
	PhasePrepare = "prepare"
	PhaseSession = "session"
	PhaseReserve = "reserve"
	PhaseUpload  = "upload"
	PhaseCommit  = "commit"
	PhaseWait    = "wait"
)

// PhaseName 返回阶段的中文说明。
func PhaseName(s string) string {
	switch s {
	case PhasePrepare:
		return "解析安装包"
	case PhaseSession:
		return "创建上传会话"
	case PhaseReserve:
		return "预留文件位"
	case PhaseUpload:
		return "上传字节"
	case PhaseCommit:
		return "提交文件"
	case PhaseWait:
		return "等待 Apple 处理"
	case "done":
		return "完成"
	case "failed":
		return "失败"
	case "canceled":
		return "已取消"
	}
	return s
}

// ErrCanceled 表示用户主动取消（区别于失败）。
var ErrCanceled = errors.New("已取消")

// Config 是认证配置。
type Config struct {
	KeyPath  string `json:"keyPath"`
	KeyID    string `json:"keyId"`
	IssuerID string `json:"issuerId"`
	APIBase  string `json:"apiBase"`
	// KeyType 密钥类型：team（团队密钥，默认）/ individual（个人密钥）。
	// JWT payload 结构不同（官方文档）：团队密钥用 iss（Issuer ID，必填）；
	// 个人密钥不用 Issuer ID，用 sub="user"。上传端点两种密钥通用。
	KeyType string `json:"keyType"`
}

// 密钥类型常量。
const (
	KeyTypeTeam       = "team"
	KeyTypeIndividual = "individual"
)

// NormalizeKeyType 规范化密钥类型（未知值一律按团队密钥处理）。
func NormalizeKeyType(s string) string {
	if strings.EqualFold(s, KeyTypeIndividual) {
		return KeyTypeIndividual
	}
	return KeyTypeTeam
}

// KeyTypeLabel 返回密钥类型的中文说明。
func KeyTypeLabel(s string) string {
	if NormalizeKeyType(s) == KeyTypeIndividual {
		return "个人密钥（无需 Issuer ID，JWT 用 sub=user，权限＝创建者）"
	}
	return "团队密钥（需 Issuer ID，团队共享）"
}

// Request 是一次上传任务的参数。
type Request struct {
	AppID    string `json:"appId"`
	IpaPath  string `json:"ipaPath"`
	Version  string `json:"version"`  // 覆盖版本号（留空则从包内读取）
	Build    string `json:"build"`    // 覆盖构建号（留空则从包内读取）
	Platform string `json:"platform"` // IOS（默认）/ MAC_OS / TV_OS / VISION_OS
	Wait     bool   `json:"wait"`     // 是否等待 Apple 处理完成

	PollInterval time.Duration `json:"-"` // 轮询间隔（默认 5s；测试可调小）
	WaitTimeout  time.Duration `json:"-"` // 等待处理超时（默认 30m）
}

// Hooks 是进度回调，字段为 nil 时忽略。所有回调在流水线 goroutine 内调用。
type Hooks struct {
	Phase    func(phase string)
	Log      func(msg string)
	Progress func(done, total int64, speedBps float64) // speedBps 为 EMA 速度
}

// Result 是上传结果。
type Result struct {
	BuildUploadID string `json:"buildUploadId"`
	BuildID       string `json:"buildId"` // 仅等待处理完成后有值
}

type runner struct {
	hooks Hooks
	speed speedEMA
}

// phase 只回调；阶段如何展示（步骤条 / 日志）由各端自己的 Phase 回调决定。
func (r *runner) phase(p string) {
	if r.hooks.Phase != nil {
		r.hooks.Phase(p)
	}
}

func (r *runner) log(format string, args ...any) {
	if r.hooks.Log != nil {
		r.hooks.Log(fmt.Sprintf(format, args...))
	}
}

// Run 同步执行完整上传流程。返回 ErrCanceled 表示用户取消。
func Run(ctx context.Context, cfg Config, req Request, hooks Hooks) (*Result, error) {
	if req.Platform == "" {
		req.Platform = "IOS"
	}
	if req.PollInterval <= 0 {
		req.PollInterval = 5 * time.Second
	}
	if req.WaitTimeout <= 0 {
		req.WaitTimeout = 30 * time.Minute
	}
	r := &runner{hooks: hooks}
	r.log("密钥类型：%s", KeyTypeLabel(cfg.KeyType))

	// 1. 解析安装包
	r.phase(PhasePrepare)
	st, err := os.Stat(req.IpaPath)
	if err != nil {
		return nil, fmt.Errorf("安装包不可访问: %w", err)
	}
	fileSize := st.Size()
	fileName := filepath.Base(req.IpaPath)
	r.log("安装包: %s（%s）", fileName, FmtBytes(fileSize))
	version, build := req.Version, req.Build
	if info, ierr := ipa.ReadInfo(req.IpaPath); ierr == nil {
		r.log("App: %s  Bundle ID: %s  版本: %s  构建: %s", info.Name, info.BundleID, info.Version, info.Build)
		if version == "" {
			version = info.Version
		}
		if build == "" {
			build = info.Build
		}
	} else {
		r.log("读取包内信息失败: %v", ierr)
	}
	uti, err := ipa.UTI(fileName)
	if err != nil {
		return nil, err
	}
	if version == "" {
		return nil, fmt.Errorf("缺少版本号（CFBundleShortVersionString）：包内未读到，请手动指定")
	}
	if build == "" {
		return nil, fmt.Errorf("缺少构建号（CFBundleVersion）：包内未读到，请手动指定")
	}

	client, err := ascapi.NewClient(ascapi.Credentials{
		KeyPath: cfg.KeyPath, KeyID: cfg.KeyID, IssuerID: cfg.IssuerID, APIBase: cfg.APIBase,
		KeyType: NormalizeKeyType(cfg.KeyType),
	})
	if err != nil {
		return nil, err
	}

	// 2. 创建上传会话
	r.phase(PhaseSession)
	r.log("创建上传会话（App %s，%s，%s build %s）…", req.AppID, req.Platform, version, build)
	buildUploadID, err := ascapi.CreateBuildUpload(ctx, client, req.AppID, version, build, req.Platform)
	if err != nil {
		return nil, ctxOrErr(ctx, err)
	}
	r.log("上传会话已创建: %s", buildUploadID)

	// 3. 预留文件位
	r.phase(PhaseReserve)
	file, err := ascapi.ReserveFile(ctx, client, buildUploadID, fileName, fileSize, uti)
	if err != nil {
		return nil, ctxOrErr(ctx, err)
	}
	r.log("获得 %d 个分片上传操作", len(file.Ops))

	// 4. 上传字节
	r.phase(PhaseUpload)
	r.log("开始上传 %s …", FmtBytes(fileSize))
	if hooks.Progress != nil {
		hooks.Progress(0, fileSize, 0)
	}
	err = ascapi.ExecuteUploadOps(ctx, req.IpaPath, file.Ops, func(done, total int64) {
		if hooks.Progress != nil {
			hooks.Progress(done, total, r.speed.sample(done))
		}
	})
	if err != nil {
		return nil, ctxOrErr(ctx, err)
	}

	// 5. 提交文件
	r.phase(PhaseCommit)
	md5Hex, err := ascapi.FileMD5(req.IpaPath)
	if err != nil {
		return nil, fmt.Errorf("计算 MD5 失败: %w", err)
	}
	if err := ascapi.CommitFile(ctx, client, file.ID, md5Hex); err != nil {
		return nil, ctxOrErr(ctx, err)
	}
	r.log("文件已提交（MD5 %s）", md5Hex)

	if !req.Wait {
		r.log("字节上传完成，会话 %s 正在 Apple 侧排队处理，可稍后在 App Store Connect 查看。", buildUploadID)
		return &Result{BuildUploadID: buildUploadID}, nil
	}

	// 6. 轮询处理状态
	r.phase(PhaseWait)
	deadline := time.Now().Add(req.WaitTimeout)
	lastState := ""
	for {
		doc, err := ascapi.GetBuildUpload(ctx, client, buildUploadID)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ErrCanceled
			}
			r.log("查询状态失败（%v），%s 后重试", err, req.PollInterval)
		} else {
			if doc.State.State != lastState {
				r.log("状态: %s", doc.State.State)
				lastState = doc.State.State
			}
			for _, e := range doc.State.Errors {
				r.log("错误 [%s] %s", e.Code, e.Description)
			}
			for _, w := range doc.State.Warnings {
				r.log("警告 [%s] %s", w.Code, w.Description)
			}
			if doc.State.State == "COMPLETE" {
				res := &Result{BuildUploadID: buildUploadID, BuildID: doc.BuildID}
				if doc.BuildID != "" {
					r.log("Build ID: %s（版本 %s，处理状态 %s）", doc.BuildID, doc.BuildVersion, doc.BuildProcessingState)
				}
				return res, nil
			}
			if doc.State.State == "FAILED" {
				msg := "Apple 处理该构建失败"
				for _, e := range doc.State.Errors {
					msg += "：" + e.Code + " " + e.Description
				}
				return nil, errors.New(msg)
			}
		}
		if time.Now().After(deadline) {
			r.log("等待超时（%s）。字节已全部上传成功，Apple 处理较慢属正常，可稍后在 App Store Connect 查看。", req.WaitTimeout)
			return &Result{BuildUploadID: buildUploadID}, nil
		}
		select {
		case <-ctx.Done():
			return nil, ErrCanceled
		case <-time.After(req.PollInterval):
		}
	}
}

// ctxOrErr 在上下文已取消时把任意错误统一为 ErrCanceled。
func ctxOrErr(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ErrCanceled
	}
	return err
}

/* ---------------- 速度 EMA 与格式化 ---------------- */

// speedEMA 由多个上传 worker 并发调用（progress 回调），必须加锁。
type speedEMA struct {
	mu       sync.Mutex
	lastTick time.Time
	lastByte int64
	ema      float64
}

func (s *speedEMA) sample(done int64) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.lastTick.IsZero() {
		s.lastTick = now
		s.lastByte = done
		return 0
	}
	dt := now.Sub(s.lastTick).Seconds()
	if dt > 0.3 {
		bps := float64(done-s.lastByte) / dt
		if bps < 0 {
			bps = 0
		}
		if s.ema > 0 {
			s.ema = 0.6*s.ema + 0.4*bps
		} else {
			s.ema = bps
		}
		s.lastTick = now
		s.lastByte = done
	}
	return s.ema
}

// FmtBytes 把字节数格式化为人类可读形式。
func FmtBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n; m >= unit; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

// FmtSpeed 把速度格式化为人类可读形式。
func FmtSpeed(bps float64) string {
	if bps <= 0 {
		return ""
	}
	units := []string{"B/s", "KB/s", "MB/s", "GB/s"}
	u := 0
	for bps >= 1024 && u < len(units)-1 {
		bps /= 1024
		u++
	}
	return fmt.Sprintf("%.1f %s", bps, units[u])
}
