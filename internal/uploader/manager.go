package uploader

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// Task 一次完整的上传任务参数（认证 + 上传请求）。
type Task struct {
	Config
	Request
}

// Job 是一个后台上传任务的实时状态（GUI 轮询的 JSON 结构）。
type Job struct {
	mu         sync.Mutex
	ctx        context.Context
	cancel     context.CancelFunc
	State      string   `json:"state"`
	BytesDone  int64    `json:"bytesDone"`
	BytesTotal int64    `json:"bytesTotal"`
	Speed      string   `json:"speed"`
	Logs       []string `json:"logs"`
	Error      string   `json:"error"`
	Result     *Result  `json:"result"`
	Finished   bool     `json:"finished"`
}

func (j *Job) log(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Logs = append(j.Logs, time.Now().Format("15:04:05")+" "+msg)
	if len(j.Logs) > 500 {
		j.Logs = j.Logs[len(j.Logs)-500:]
	}
}

func (j *Job) fail(msg string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = "failed"
	j.Error = msg
	j.Finished = true
}

func (j *Job) succeed(res *Result) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = "done"
	j.Result = res
	j.Finished = true
}

func (j *Job) markCanceled() {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.State = "canceled"
	j.Finished = true
}

// IsFinished 在 j.mu 下读取完成标记（供 JobManager 等跨锁场景使用）。
func (j *Job) IsFinished() bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.Finished
}

// Snapshot 返回当前任务快照（nil 接收者返回 nil）。
func (j *Job) Snapshot() *JobSnapshot {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.dynamicCopy()
}

// JobSnapshot 是可安全序列化的任务快照（GUI 轮询的 JSON 结构）。
type JobSnapshot struct {
	State      string   `json:"state"`
	BytesDone  int64    `json:"bytesDone"`
	BytesTotal int64    `json:"bytesTotal"`
	Speed      string   `json:"speed"`
	Logs       []string `json:"logs"`
	Error      string   `json:"error"`
	Result     *Result  `json:"result"`
	Finished   bool     `json:"finished"`
}

func (j *Job) dynamicCopy() *JobSnapshot {
	return &JobSnapshot{
		State:      j.State,
		BytesDone:  j.BytesDone,
		BytesTotal: j.BytesTotal,
		Speed:      j.Speed,
		Logs:       append([]string(nil), j.Logs...),
		Error:      j.Error,
		Result:     j.Result,
		Finished:   j.Finished,
	}
}

// JobManager 管理单个后台上传任务（同一时刻只允许一个）。
type JobManager struct {
	mu  sync.Mutex
	job *Job
}

// NewJobManager 创建任务管理器。
func NewJobManager() *JobManager { return &JobManager{} }

// Start 启动后台上传任务；已有任务进行中时返回错误。
func (m *JobManager) Start(task Task) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && !m.job.IsFinished() {
		return fmt.Errorf("已有上传任务进行中，请先等待完成或取消")
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &Job{ctx: ctx, cancel: cancel, State: PhasePrepare, Logs: []string{}}
	m.job = job
	go m.run(job, task)
	return nil
}

// Cancel 请求取消当前任务。
func (m *JobManager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.job != nil && !m.job.IsFinished() {
		m.job.cancel()
	}
}

// Current 返回当前任务（可能为 nil）。
func (m *JobManager) Current() *Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.job
}

func (m *JobManager) run(job *Job, task Task) {
	defer func() {
		if r := recover(); r != nil {
			job.fail(fmt.Sprintf("内部错误: %v", r))
		}
	}()
	res, err := Run(job.ctx, task.Config, task.Request, Hooks{
		Phase: func(p string) {
			job.mu.Lock()
			job.State = p
			job.mu.Unlock()
			job.log("—— " + PhaseName(p))
		},
		Log: job.log,
		Progress: func(done, total int64, bps float64) {
			job.mu.Lock()
			defer job.mu.Unlock()
			job.BytesDone = done
			job.BytesTotal = total
			job.Speed = FmtSpeed(bps)
		},
	})
	switch {
	case err == ErrCanceled:
		job.markCanceled()
	case err != nil:
		job.fail(err.Error())
	default:
		job.succeed(res)
	}
}
