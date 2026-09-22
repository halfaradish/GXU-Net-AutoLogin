// Package logging 提供带时间戳与级别的日志输出：控制台、轮转文件、内存环形缓冲。
//
// 三个输出互相独立：GUI 子系统下没有控制台，就只留文件与内存缓冲；文件不可写时
// 降级为"仅控制台/内存"，绝不因为写日志失败而把守护进程带崩。
package logging

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// DefaultDir 是日志的默认目录
	DefaultDir = "logs"
	// DefaultFileName 是日志的默认文件名
	DefaultFileName = "GXU_Net_AutoLogin.log"
	// DefaultMaxSize 是单个日志文件的上限
	DefaultMaxSize = 5 << 20
	// DefaultBackups 是保留的轮转份数：.1、.2（更老的删除）
	DefaultBackups = 2
	// DefaultRingLines 是内存里保留的日志条数（供界面实时查看）
	DefaultRingLines = 2000

	timeLayout = "2006-01-02 15:04:05"
)

// Level 是日志级别
type Level int

const (
	// Info 级别
	Info Level = iota
	// Warn 级别
	Warn
	// Error 级别
	Error
)

// String 返回级别名，写进日志文件的那三个词
func (l Level) String() string {
	switch l {
	case Warn:
		return "WARN"
	case Error:
		return "ERROR"
	default:
		return "INFO"
	}
}

// Line 是一条日志
type Line struct {
	At      time.Time
	Level   Level
	Message string
}

// String 还原成写入文件时的整行文本
func (ln Line) String() string {
	return ln.At.Format(timeLayout) + " [" + ln.Level.String() + "] " + ln.Message
}

// Options 是 Logger 的构造参数
type Options struct {
	// Console 为 nil 表示不输出到控制台（托盘版就是这种情况）
	Console io.Writer

	// MaxSize 为 0 时使用 DefaultMaxSize
	MaxSize int64

	// Backups 为 0 时使用 DefaultBackups
	Backups int

	// RingLines 为 0 时使用 DefaultRingLines
	RingLines int
}

// Logger 是并发安全的日志器
type Logger struct {
	mu      sync.Mutex
	console io.Writer
	ring    []Line
	ringCap int
	total   uint64

	file    *rollingFile
	path    string
	maxSize int64
	backups int
}

// New 创建日志器；此时还没有日志文件，需要写文件就再调用 RetargetFile
func New(opts Options) *Logger {
	if opts.MaxSize <= 0 {
		opts.MaxSize = DefaultMaxSize
	}
	if opts.Backups <= 0 {
		opts.Backups = DefaultBackups
	}
	if opts.RingLines <= 0 {
		opts.RingLines = DefaultRingLines
	}
	return &Logger{
		console: opts.Console,
		ringCap: opts.RingLines,
		maxSize: opts.MaxSize,
		backups: opts.Backups,
	}
}

// Logf 记录一条日志
func (l *Logger) Logf(lv Level, format string, args ...any) {
	l.emit(lv, fmt.Sprintf(format, args...))
}

// Info 记录一条 INFO 日志
func (l *Logger) Info(format string, args ...any) { l.Logf(Info, format, args...) }

// Warn 记录一条 WARN 日志
func (l *Logger) Warn(format string, args ...any) { l.Logf(Warn, format, args...) }

// Error 记录一条 ERROR 日志
func (l *Logger) Error(format string, args ...any) { l.Logf(Error, format, args...) }

// MaxSizeMiB 返回单个日志文件上限（MiB）
func (l *Logger) MaxSizeMiB() int64 { return l.maxSize >> 20 }

// Backups 返回保留的轮转份数
func (l *Logger) Backups() int { return l.backups }

// FilePath 返回当前日志文件路径；未启用文件日志时为空
func (l *Logger) FilePath() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.path
}

// SetConsole 更换控制台输出目标；传 nil 表示不输出到控制台
func (l *Logger) SetConsole(w io.Writer) {
	l.mu.Lock()
	l.console = w
	l.mu.Unlock()
}

// RetargetFile 打开（或切换）日志文件，path 为空时用默认位置 logs/GXU_Net_AutoLogin.log。
// 返回实际使用的绝对路径；失败时返回错误且原有输出不受影响。
func (l *Logger) RetargetFile(path string) (string, error) {
	if path == "" {
		path = filepath.Join(DefaultDir, DefaultFileName)
	}

	// 目录不存在就建：默认的 logs/，或配置里写的多级路径
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", fmt.Errorf("无法创建日志目录 %s：%v", dir, err)
		}
	}

	f, err := openRollingFile(path, l.maxSize, l.backups)
	if err != nil {
		return "", fmt.Errorf("无法写入日志文件 %s：%v", path, err)
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	l.mu.Lock()
	old := l.file
	l.file, l.path = f, abs
	l.mu.Unlock()

	// 换目标前关掉旧文件，避免重复调用时泄漏句柄
	old.Close()
	return abs, nil
}

// DisableFile 关闭并停用日志文件，只保留控制台与内存缓冲
func (l *Logger) DisableFile() {
	l.mu.Lock()
	f := l.file
	l.file, l.path = nil, ""
	l.mu.Unlock()
	f.Close()
}

// ClearFile 清空日志：截断当前文件、删除轮转备份，并清空内存缓冲
func (l *Logger) ClearFile() error {
	l.mu.Lock()
	f, path := l.file, l.path
	l.ring = l.ring[:0]
	l.total = 0
	backups := l.backups
	l.mu.Unlock()

	var err error
	if f != nil {
		err = f.Truncate()
	}
	// 轮转备份一并删除：否则会出现"清空了却还有 .1"的困惑
	if path != "" {
		for i := 1; i <= backups; i++ {
			os.Remove(fmt.Sprintf("%s.%d", path, i))
		}
	}
	if err != nil {
		return err
	}

	l.Info("日志已清空")
	return nil
}

// Snapshot 返回内存缓冲里的日志副本与累计条数，供界面增量刷新
func (l *Logger) Snapshot() ([]Line, uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()

	out := make([]Line, len(l.ring))
	copy(out, l.ring)
	return out, l.total
}

// Close 关闭日志文件
func (l *Logger) Close() error {
	l.mu.Lock()
	f := l.file
	l.file, l.path = nil, ""
	l.mu.Unlock()
	return f.Close()
}

func (l *Logger) emit(lv Level, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.emitLocked(lv, time.Now(), msg)
}

func (l *Logger) emitLocked(lv Level, at time.Time, msg string) {
	line := Line{At: at, Level: lv, Message: msg}

	// 内存缓冲先记：就算下面写文件失败，界面上也还看得到这条
	l.ringAppendLocked(line)

	text := line.String() + "\n"
	if l.console != nil {
		io.WriteString(l.console, text)
	}
	if l.file == nil {
		return
	}

	if _, err := l.file.Write([]byte(text)); err != nil {
		// 日志文件不可写不能拖垮守护进程：报一次，之后只打印到控制台
		f := l.file
		l.file, l.path = nil, ""
		f.Close()

		warn := Line{At: time.Now(), Level: Warn, Message: fmt.Sprintf("日志文件写入失败，后续仅打印到控制台: %v", err)}
		l.ringAppendLocked(warn)
		if l.console != nil {
			io.WriteString(l.console, warn.String()+"\n")
		}
	}
}

func (l *Logger) ringAppendLocked(ln Line) {
	l.total++
	if l.ringCap <= 0 {
		return
	}
	if len(l.ring) < l.ringCap {
		l.ring = append(l.ring, ln)
		return
	}
	copy(l.ring, l.ring[1:])
	l.ring[len(l.ring)-1] = ln
}
