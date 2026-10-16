package tunnel

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	frplog "github.com/fatedier/frp/pkg/util/log"
	golog "github.com/fatedier/golib/log"

	"frpdesk/internal/configstore"
)

// LogRing 线程安全的日志环形缓冲（对齐老版 logs 上限 500 条）。
type LogRing struct {
	mu      sync.Mutex
	max     int
	entries []configstore.LogEntry
	nextID  uint64
}

// NewLogRing 创建容量为 max 的环形缓冲。
func NewLogRing(max int) *LogRing {
	return &LogRing{max: max, entries: make([]configstore.LogEntry, 0, max)}
}

// Append 追加一条日志，超出容量丢弃最旧的，返回本次新增的条目。
func (r *LogRing) Append(level, message string) []configstore.LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendLocked(level, message, "", "")
}

// AppendServer 追加一条可归属实例的日志（ServerID/ServerName 随条目持久进环形缓冲）。
// 仅用于能准确归属实例的条目；无法归属的走 Append（宁缺勿错标）。
func (r *LogRing) AppendServer(level, message, serverID, serverName string) []configstore.LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.appendLocked(level, message, serverID, serverName)
}

func (r *LogRing) appendLocked(level, message, serverID, serverName string) []configstore.LogEntry {
	added := make([]configstore.LogEntry, 0)
	for _, line := range splitLines(message) {
		r.nextID++
		added = append(added, configstore.LogEntry{
			ID:         r.nextID,
			Level:      level,
			Message:    line,
			Timestamp:  time.Now().Format(time.RFC3339),
			ServerID:   serverID,
			ServerName: serverName,
		})
	}
	r.entries = append(r.entries, added...)
	if overflow := len(r.entries) - r.max; overflow > 0 {
		r.entries = r.entries[overflow:]
	}
	return added
}

// All 返回全部缓冲日志的副本。
func (r *LogRing) All() []configstore.LogEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]configstore.LogEntry, len(r.entries))
	copy(out, r.entries)
	return out
}

// splitLines 按行拆分并去掉空行（对齐老版 pushLog 的分行逻辑）。
func splitLines(message string) []string {
	raw := strings.Split(message, "\n")
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// frpLogSink 实现 golib log.Writer 接口，把 frp 结构化日志异步接入环形缓冲。
// golib logger 的 log() 在持有 outMu 的情况下调用 WriteLog，若在锁内同步回调宿主，
// 宿主回调链路中再触发 frp 写日志就会死锁（与 Manager.mu 同类隐患）。
// 因此 WriteLog 只入队，由独立消费 goroutine 在无锁状态下回调；队列满则丢弃并计数。
type frpLogSink struct {
	onAppend func(level, message string)
	ch       chan logMsg
	dropped  atomic.Uint64
}

type logMsg struct {
	level   string
	message string
}

const logQueueSize = 1024

func newFrpLogSink(onAppend func(level, message string)) *frpLogSink {
	s := &frpLogSink{
		onAppend: onAppend,
		ch:       make(chan logMsg, logQueueSize),
	}
	go s.drain()
	return s
}

// WriteLog 满足 golib log.Writer 接口，级别信息零损失。
func (s *frpLogSink) WriteLog(p []byte, level golog.Level, when time.Time) (int, error) {
	lv := "info"
	switch {
	case level >= golog.ErrorLevel:
		lv = "error"
	case level >= golog.WarnLevel:
		lv = "warn" // 修复：WARN 此前被并入 info
	}
	s.enqueue(lv, string(p))
	return len(p), nil
}

// Write 满足 io.Writer（WithOutput 参数类型要求）；正常流程不会走到，兜底按 error 处理。
func (s *frpLogSink) Write(p []byte) (int, error) {
	s.enqueue("error", string(p))
	return len(p), nil
}

func (s *frpLogSink) enqueue(level, message string) {
	msg := logMsg{level: level, message: message}
	select {
	case s.ch <- msg:
	default:
		dropped := s.dropped.Add(1)
		fmt.Fprintf(os.Stderr, "[%s] [WARN] frp log queue is full; dropped logs (total %d)\n",
			time.Now().Format("2006-01-02 15:04:05.000"), dropped)
	}
}

// drain 在无任何内部锁的状态下逐条回调宿主。
func (s *frpLogSink) drain() {
	for msg := range s.ch {
		if s.onAppend != nil {
			s.onAppend(msg.level, msg.message)
		}
	}
}

// installLogSink 把 frp 全局 logger 的输出接管到内存环形缓冲。
// 只在首次调用时生效（幂等）；必须在 client.Service.Run 之前完成，
// 运行期不再替换 Logger 指针，避免数据竞争。
var logSinkOnce sync.Once

func installLogSink(onAppend func(level, message string)) {
	logSinkOnce.Do(func() {
		frplog.Logger = frplog.Logger.WithOptions(golog.WithOutput(newFrpLogSink(onAppend)))
	})
}
