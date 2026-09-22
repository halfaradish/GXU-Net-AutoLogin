//go:build windows

package main

import (
	"errors"
	"testing"

	"github.com/halfaradish/GXU-Net-AutoLogin/internal/config"
)

// 提醒只剩启动与进托盘两处，这里盯住启动那一处：只有"配置齐全 + 启动后不显示
// 主界面 + 没带 -show"才静默驻留（弹气泡），其余情况都必须把主界面摆出来 ——
// 否则用户既看不到界面、也等不到气泡，会以为程序没起来。
func TestBackgroundStartOnlyWhenWindowStaysHidden(t *testing.T) {
	cases := []struct {
		name       string
		show       bool
		user, pass string
		loadErr    bool
		startHide  bool
		want       bool
	}{
		{name: "配置齐全且勾了启动后不显示主界面", user: "u", pass: "p", startHide: true, want: true},
		{name: "带 -show 就要显示主界面", show: true, user: "u", pass: "p", startHide: true},
		{name: "没勾启动后不显示主界面", user: "u", pass: "p"},
		{name: "还没填账号密码", startHide: true},
		{name: "配置读不动", user: "u", pass: "p", startHide: true, loadErr: true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.User, cfg.Password = c.user, c.pass
			cfg.StartMinimized = c.startHide

			a := &app{cfg: cfg}
			if c.loadErr {
				a.loadErr = errors.New("读不到配置")
			}

			if got := a.backgroundStart(c.show); got != c.want {
				t.Errorf("backgroundStart(show=%v) = %v，期望 %v", c.show, got, c.want)
			}
		})
	}
}
