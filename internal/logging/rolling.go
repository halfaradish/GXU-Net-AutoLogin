package logging

import (
	"fmt"
	"os"
)

// rollingFile 是按大小轮转的追加写入器：写满后把当前文件改名为 .1，
// 原来的 .1 变 .2，更老的丢弃，然后重开一个空文件继续写。
type rollingFile struct {
	path    string
	maxSize int64
	backups int
	f       *os.File
	size    int64
}

func openRollingFile(path string, maxSize int64, backups int) (*rollingFile, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	// 追加模式：重启后接着写同一个文件，大小以现有内容为起点
	size := int64(0)
	if st, err := f.Stat(); err == nil {
		size = st.Size()
	}

	if maxSize <= 0 {
		maxSize = DefaultMaxSize
	}
	if backups < 0 {
		backups = 0
	}
	return &rollingFile{path: path, maxSize: maxSize, backups: backups, f: f, size: size}, nil
}

func (w *rollingFile) Write(p []byte) (int, error) {
	if w.size+int64(len(p)) > w.maxSize {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *rollingFile) rotate() error {
	w.f.Close()

	// 改名是尽力而为：被别的进程占用（例如编辑器打开着 .1）时只是这一轮没轮转成功，
	// 后面重开的文件继续写，不影响日志本身
	if w.backups > 0 {
		os.Remove(fmt.Sprintf("%s.%d", w.path, w.backups))
		for i := w.backups - 1; i >= 1; i-- {
			os.Rename(fmt.Sprintf("%s.%d", w.path, i), fmt.Sprintf("%s.%d", w.path, i+1))
		}
		os.Rename(w.path, w.path+".1")
	}

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	return nil
}

// Truncate 清空当前文件并把大小计数归零（对应界面上的"清空日志"）
func (w *rollingFile) Truncate() error {
	if w == nil || w.f == nil {
		return nil
	}
	w.f.Close()

	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	w.f, w.size = f, 0
	return nil
}

func (w *rollingFile) Close() error {
	if w == nil || w.f == nil {
		return nil
	}
	return w.f.Close()
}
