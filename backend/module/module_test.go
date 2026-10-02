package module

import (
	"context"
	"strings"
	"testing"

	besdk "github.com/brickKit/be-sdk-go"
)

// 构造函数遇到缺失或不合法的配置必须返回 error，不能 panic：进了外壳，
// 成员构造函数返回的 error 由外壳点名报出并按失败契约退出；panic 则绕过
// 这条路径，整个外壳带着堆栈崩掉，日志里也看不出是哪个成员的哪个键。
func newWithConfig(t *testing.T, values map[string]string) (err error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("New 应该返回 error，实际 panic：%v", r)
		}
	}()
	_, err = New(context.Background(), &besdk.Runtime{Config: besdk.NewConfig(values)})
	return err
}

func validConfig() map[string]string {
	return map[string]string{
		"DINGTALK_APP_KEY":    "ak",
		"DINGTALK_APP_SECRET": "as",
		"DINGTALK_AGENT_ID":   "123",
	}
}

func TestNew_缺必填的钉钉配置时返回error而不是panic(t *testing.T) {
	for _, key := range []string{"DINGTALK_APP_KEY", "DINGTALK_APP_SECRET", "DINGTALK_AGENT_ID"} {
		t.Run(key, func(t *testing.T) {
			cfg := validConfig()
			delete(cfg, key)
			err := newWithConfig(t, cfg)
			if err == nil {
				t.Fatalf("缺 %s 时期望返回 error", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("错误信息应点名 %s，实际：%v", key, err)
			}
		})
	}
}

func TestNew_DINGTALK_AGENT_ID不是整数时返回error(t *testing.T) {
	cfg := validConfig()
	cfg["DINGTALK_AGENT_ID"] = "not-a-number"
	err := newWithConfig(t, cfg)
	if err == nil {
		t.Fatal("DINGTALK_AGENT_ID 不是整数时期望返回 error")
	}
	if !strings.Contains(err.Error(), "DINGTALK_AGENT_ID") {
		t.Fatalf("错误信息应点名 DINGTALK_AGENT_ID，实际：%v", err)
	}
}
