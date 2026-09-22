package logging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWritesTimestampedLinesToFileAndRing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.log")

	l := New(Options{RingLines: 10})
	defer l.Close()
	if _, err := l.RetargetFile(path); err != nil {
		t.Fatalf("RetargetFile 失败：%v", err)
	}
	l.Info("你好 %s", "世界")
	l.Warn("注意")
	l.Error("出错")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{"[INFO] 你好 世界", "[WARN] 注意", "[ERROR] 出错"} {
		if !strings.Contains(text, want) {
			t.Fatalf("日志文件里缺少 %q：\n%s", want, text)
		}
	}

	lines, total := l.Snapshot()
	if total != 3 || len(lines) != 3 {
		t.Fatalf("环形缓冲应有 3 条，实际 total=%d len=%d", total, len(lines))
	}
	if lines[0].Level != Info || lines[2].Level != Error {
		t.Fatalf("级别记录不对：%+v", lines)
	}
}

func TestRingKeepsOnlyLatestLines(t *testing.T) {
	l := New(Options{RingLines: 3})
	for _, s := range []string{"1", "2", "3", "4", "5"} {
		l.Info(s)
	}

	lines, total := l.Snapshot()
	if total != 5 {
		t.Fatalf("累计条数应为 5，实际 %d", total)
	}
	if len(lines) != 3 || lines[0].Message != "3" || lines[2].Message != "5" {
		t.Fatalf("环形缓冲应只留最后 3 条，实际 %+v", lines)
	}
}

func TestRotatesAndKeepsBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "r.log")

	// 单文件上限压到很小，方便触发轮转
	l := New(Options{MaxSize: 200, Backups: 2})
	defer l.Close()
	if _, err := l.RetargetFile(path); err != nil {
		t.Fatalf("RetargetFile 失败：%v", err)
	}
	for i := 0; i < 20; i++ {
		l.Info("这是一条比较长的日志，用来把文件撑大 %d", i)
	}

	for _, want := range []string{path, path + ".1"} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("轮转后应存在 %s：%v", want, err)
		}
	}
	// 只保留 backups 份：.3 不该出现
	if _, err := os.Stat(path + ".3"); err == nil {
		t.Fatal("轮转备份超过了保留份数")
	}
}

func TestClearFileTruncatesAndDropsBackups(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "c.log")

	l := New(Options{MaxSize: 200, Backups: 2})
	defer l.Close()
	if _, err := l.RetargetFile(path); err != nil {
		t.Fatalf("RetargetFile 失败：%v", err)
	}
	for i := 0; i < 20; i++ {
		l.Info("写点东西把文件撑大 %d", i)
	}
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("先要产生一个备份，实际：%v", err)
	}

	if err := l.ClearFile(); err != nil {
		t.Fatalf("ClearFile 失败：%v", err)
	}

	if _, err := os.Stat(path + ".1"); err == nil {
		t.Fatal("清空日志时应一并删掉轮转备份")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// 清空后只剩一条"日志已清空"的提示，文件应该很小
	if len(raw) > 200 || !strings.Contains(string(raw), "日志已清空") {
		t.Fatalf("清空后的文件内容不对（%d 字节）：%s", len(raw), raw)
	}

	// 清空后计数也跟着归零，界面的增量刷新才不会错乱
	lines, total := l.Snapshot()
	if total != 1 || len(lines) != 1 {
		t.Fatalf("清空后应只剩提示这一条，实际 total=%d len=%d", total, len(lines))
	}
}

func TestRetargetFileClosesPreviousHandle(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.log")
	second := filepath.Join(dir, "second.log")

	l := New(Options{})
	defer l.Close()
	if _, err := l.RetargetFile(first); err != nil {
		t.Fatal(err)
	}
	l.Info("写在第一个文件里")

	if _, err := l.RetargetFile(second); err != nil {
		t.Fatal(err)
	}
	l.Info("写在第二个文件里")

	// 换目标后，旧文件不该再被写入
	raw, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "第二个") {
		t.Fatal("换目标后仍在往旧文件写")
	}

	// 旧的句柄若没关，Windows 上会拒绝改名——用改名来验证确实关掉了
	if err := os.Rename(first, first+".moved"); err != nil {
		t.Fatalf("旧日志文件仍被占用（句柄没关）：%v", err)
	}
	if got := l.FilePath(); got != second {
		t.Fatalf("FilePath 应为 %s，实际 %s", second, got)
	}
}

func TestDefaultPathAndFailureDoesNotBreak(t *testing.T) {
	dir := t.TempDir()
	// Windows 上进程的工作目录本身会让临时目录删不掉，测完得切回去
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	l := New(Options{})
	defer l.Close()
	// 留空 → 默认 logs/GXU_Net_AutoLogin.log，目录不存在会自动创建
	abs, err := l.RetargetFile("")
	if err != nil {
		t.Fatalf("默认路径应能自动建目录：%v", err)
	}
	if !strings.HasSuffix(abs, filepath.Join(DefaultDir, DefaultFileName)) {
		t.Fatalf("默认路径不对：%s", abs)
	}

	// 打不开的路径：返回错误，但已有的输出不受影响
	bad := filepath.Join(dir, "no-such-dir", "x.log")
	if _, err := os.Stat(filepath.Dir(bad)); err == nil {
		t.Fatal("前提不成立")
	}
	if _, err := l.RetargetFile(filepath.Join("\x00bad", "x.log")); err == nil {
		t.Fatal("非法路径应当报错")
	}
	l.Info("日志器仍然可用")
	if _, total := l.Snapshot(); total == 0 {
		t.Fatal("出错之后日志器不该失效")
	}
}
