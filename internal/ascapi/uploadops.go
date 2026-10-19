package ascapi

import (
	"bytes"
	"context"
	"crypto/md5"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// uploadConcurrency 是分片上传的并发 worker 数。
const uploadConcurrency = 4

// uploadAttempts 是单个分片的最多尝试次数（指数退避）。
const uploadAttempts = 4

// uploadHTTPClient 全局复用：大分片超时放宽到 15 分钟，连接池跨请求复用。
var uploadHTTPClient = &http.Client{Timeout: 15 * time.Minute}

// ExecuteUploadOps 按 Apple 下发的 uploadOperations 并发上传文件字节
// （官方流程第 3 步）。预签名 URL 不带 JWT，只按 requestHeaders 指定头发送。
// progress 在每片成功后回调（当前累计字节, 总字节）。
func ExecuteUploadOps(ctx context.Context, filePath string, ops []UploadOperation, progress func(done, total int64)) error {
	var total int64
	for _, op := range ops {
		total += op.Length
	}
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	var done atomic.Int64
	idx := make(chan int, len(ops))
	for i := range ops {
		idx <- i
	}
	close(idx)

	workers := uploadConcurrency
	if len(ops) < workers {
		workers = len(ops)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 1)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range idx {
				op := ops[i]
				if err := uploadOneOp(ctx, f, op); err != nil {
					select {
					case errCh <- fmt.Errorf("分片 %d 上传失败: %w", op.PartNumber, err):
					default:
					}
					return
				}
				d := done.Add(op.Length)
				progress(d, total)
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	progress(total, total)
	return nil
}

// uploadOneOp 上传单个分片：小分片整块读入内存，大分片走 SectionReader 流式。
func uploadOneOp(ctx context.Context, f *os.File, op UploadOperation) error {
	var lastErr error
	for attempt := 0; attempt < uploadAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(1<<(attempt-1)) * time.Second):
			}
		}
		var body io.Reader
		if op.Length <= 64<<20 {
			buf := make([]byte, op.Length)
			if _, err := f.ReadAt(buf, op.Offset); err != nil {
				return err
			}
			body = bytes.NewReader(buf)
		} else {
			body = io.NewSectionReader(f, op.Offset, op.Length)
		}
		method := op.Method
		if method == "" {
			method = http.MethodPut
		}
		req, err := http.NewRequestWithContext(ctx, method, op.URL, body)
		if err != nil {
			return err
		}
		req.ContentLength = op.Length
		for _, h := range op.RequestHeaders {
			if strings.EqualFold(h.Name, "Content-Length") {
				continue // 由 net/http 自行计算
			}
			req.Header.Set(h.Name, h.Value)
		}
		res, err := uploadHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		res.Body.Close()
		if res.StatusCode >= 200 && res.StatusCode < 300 {
			return nil
		}
		if res.StatusCode >= 500 || res.StatusCode == 429 {
			lastErr = fmt.Errorf("HTTP %d: %s", res.StatusCode, truncate(string(data), 200))
			continue
		}
		return fmt.Errorf("HTTP %d: %s", res.StatusCode, truncate(string(data), 300))
	}
	return lastErr
}

// FileMD5 流式计算文件 MD5（提交文件时的完整性校验值）。
func FileMD5(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := md5.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
