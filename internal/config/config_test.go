package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseDefaultsAndUnknownKeys(t *testing.T) {
	cfg, err := Parse("# 注释\nUSER=u1\nPASSWORD=p1\nNOT_A_KEY=whatever\nLOG_TO_FILE=\n")
	if err != nil {
		t.Fatalf("Parse 出错：%v", err)
	}
	if cfg.User != "u1" || cfg.Password != "p1" {
		t.Fatalf("账号密码解析错误：%+v", cfg)
	}
	// 没写的键保留默认值：托盘版默认"最小化到托盘 + 启动不弹窗"，不开机自启
	if !cfg.MinimizeToTray || !cfg.StartMinimized || cfg.Autostart {
		t.Fatalf("默认值不对：%+v", cfg)
	}
	if cfg.LogToFile {
		t.Fatal("LOG_TO_FILE 留空应视为不写文件")
	}
}

func TestParseBoolVariantsAndErrors(t *testing.T) {
	for _, v := range []string{"true", "1", "YES", "on"} {
		cfg, err := Parse("LOG_TO_FILE=" + v)
		if err != nil || !cfg.LogToFile {
			t.Fatalf("%q 应解析为 true（err=%v）", v, err)
		}
	}
	for _, v := range []string{"false", "0", "No", "off"} {
		cfg, err := Parse("AUTOSTART=" + v)
		if err != nil || cfg.Autostart {
			t.Fatalf("%q 应解析为 false（err=%v）", v, err)
		}
	}
	if _, err := Parse("LOG_TO_FILE=maybe"); err == nil {
		t.Fatal("非法布尔值应当报错")
	}
}

func TestParseQuotedValuesAndCaseInsensitiveKeys(t *testing.T) {
	cfg, err := Parse(`user = " 1807210721 "` + "\n" + `PassWord='p#1=2'` + "\n")
	if err != nil {
		t.Fatalf("Parse 出错：%v", err)
	}
	if cfg.User != " 1807210721 " {
		t.Fatalf("引号内的空格应原样保留：%q", cfg.User)
	}
	if cfg.Password != "p#1=2" {
		t.Fatalf("密码解析错误：%q", cfg.Password)
	}
}

// 保存再读回来必须一模一样——界面保存配置靠的就是这个
func TestSaveReadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := &Config{
		User:           "1807210721",
		Password:       `p#ss "word"`,
		NetType:        "telecom",
		RouterIP:       "172.16.6.6",
		RouterMAC:      "36:88:8A:99:A4:CC",
		MacAddress:     "AA:BB:CC:DD:EE:FF",
		LogToFile:      true,
		LogPath:        `D:\logs\GXU_Net_AutoLogin.log`,
		Autostart:      true,
		MinimizeToTray: false,
		StartMinimized: true,
	}

	if err := Save(dir, want); err != nil {
		t.Fatalf("Save 失败：%v", err)
	}
	got, err := Read(dir)
	if err != nil {
		t.Fatalf("Read 失败：%v", err)
	}
	if *got != *want {
		t.Fatalf("往返后不一致：\n got=%+v\nwant=%+v", *got, *want)
	}

	// 注释模板要保留，用户还能继续手工编辑
	raw, err := os.ReadFile(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# 用户名：", "# 运营商选择", "# 关闭窗口时最小化至托盘"} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("保存后的文件缺少注释 %q", want)
		}
	}
	// 临时文件不能留下
	if _, err := os.Stat(Path(dir) + ".tmp"); err == nil {
		t.Fatal("保存后残留了 .tmp 临时文件")
	}
}

// Load：文件不存在时生成模板并报错；发现旧 config.txt 时提示改名
func TestLoadCreatesTemplateAndHintsLegacy(t *testing.T) {
	dir := t.TempDir()

	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "已创建") {
		t.Fatalf("首次加载应提示已创建模板，实际：%v", err)
	}
	if _, statErr := os.Stat(Path(dir)); statErr != nil {
		t.Fatalf("模板没被创建：%v", statErr)
	}

	// 删掉 .env、放一个老 config.txt：应当提示改名而不是重新建模板
	if err := os.Remove(Path(dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, LegacyFileName), []byte("USER=u\n"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err = Load(dir)
	if err == nil || !strings.Contains(err.Error(), LegacyFileName) {
		t.Fatalf("应提示把 %s 改名，实际：%v", LegacyFileName, err)
	}
	if _, statErr := os.Stat(Path(dir)); statErr == nil {
		t.Fatal("检测到旧配置文件时不该再生成新的 .env")
	}
}

func TestLoadValidatesRequiredFields(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(Path(dir), []byte("USER=u1\nPASSWORD=\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "用户名和密码") {
		t.Fatalf("缺少密码应报错，实际：%v", err)
	}

	if err := os.WriteFile(Path(dir), []byte("USER=u1\nPASSWORD=p1\nNET_TYPE=foo\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil || !strings.Contains(err.Error(), "运营商") {
		t.Fatalf("非法运营商应报错，实际：%v", err)
	}
}

func TestReadMissingFileIsNotExist(t *testing.T) {
	_, err := Read(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("缺文件时应能被 errors.Is(…, os.ErrNotExist) 识别，实际：%v", err)
	}
}

func TestValidNetType(t *testing.T) {
	for _, ok := range []string{"", "telecom", "UNICOM", "cmcc"} {
		if !ValidNetType(ok) {
			t.Fatalf("%q 应合法", ok)
		}
	}
	for _, bad := range []string{"dx", "telecom ", "unicom@cmcc"} {
		if ValidNetType(bad) {
			t.Fatalf("%q 应非法", bad)
		}
	}
}
