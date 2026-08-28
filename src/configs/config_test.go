package configs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/pkg/ratelimit"
	"github.com/bililive-go/bililive-go/src/types"
	"github.com/stretchr/testify/assert"
)

func TestNewConfig(t *testing.T) {
	file := "../../config.yml"
	c, err := NewConfigWithFile("../../config.yml")
	assert.NoError(t, err)
	assert.Equal(t, file, c.File)
}

func TestRPC_Verify(t *testing.T) {
	var rpc *RPC
	assert.NoError(t, rpc.verify())
	rpc = new(RPC)
	rpc.Bind = "foo@bar"
	assert.NoError(t, rpc.verify())
	rpc.Enable = true
	assert.Error(t, rpc.verify())
}

func TestConfig_Verify(t *testing.T) {
	var cfg *Config
	assert.Error(t, cfg.Verify())
	cfg = &Config{
		RPC:        defaultRPC,
		Interval:   30,
		OutPutPath: os.TempDir(),
		Danmaku:    defaultDanmakuConfig,
	}
	assert.NoError(t, cfg.Verify())
	cfg.Interval = 0
	assert.Error(t, cfg.Verify())
	cfg.Interval = 30
	cfg.OutPutPath = "foobar"
	assert.Error(t, cfg.Verify())
	cfg.OutPutPath = os.TempDir()
	cfg.RPC.Enable = false
	assert.Error(t, cfg.Verify())
}

func TestDefaultUpdateConfig(t *testing.T) {
	cfg := NewConfig()
	assert.False(t, cfg.Update.AutoCheck)
	assert.Equal(t, 6, cfg.Update.CheckIntervalHours)
	assert.False(t, cfg.Update.AutoDownload)
	assert.False(t, cfg.Update.IncludePrerelease)

	configured, err := NewConfigWithBytes([]byte(`
update:
  auto_check: true
  auto_download: true
`))
	assert.NoError(t, err)
	assert.True(t, configured.Update.AutoCheck)
	assert.True(t, configured.Update.AutoDownload)
}

func TestResolveConfigForRoom(t *testing.T) {
	cfg := &Config{
		Interval:   60,
		OutPutPath: "/global",
		FfmpegPath: "/usr/bin/ffmpeg",
		PlatformConfigs: map[string]PlatformConfig{
			"douyin": {
				OverridableConfig: OverridableConfig{
					Interval:   intPtr(30),
					OutPutPath: stringPtr("/douyin"),
				},
			},
		},
	}

	room := &LiveRoom{
		Url: "https://live.douyin.com/123456",
		OverridableConfig: OverridableConfig{
			Interval: intPtr(15),
		},
	}

	resolved := cfg.ResolveConfigForRoom(room, "douyin")

	// Room-level override should take precedence
	assert.Equal(t, 15, resolved.Interval)
	// Platform-level override should take precedence over global
	assert.Equal(t, "/douyin", resolved.OutPutPath)
	// Global value should be used when no override exists
	assert.Equal(t, "/usr/bin/ffmpeg", resolved.FfmpegPath)
}

func TestGetPlatformMinAccessInterval(t *testing.T) {
	cfg := &Config{
		PlatformConfigs: map[string]PlatformConfig{
			"douyin": {
				OverridableConfig:    OverridableConfig{},
				MinAccessIntervalSec: 5,
			},
		},
	}

	// Test existing platform
	interval := cfg.GetPlatformMinAccessInterval("douyin")
	assert.Equal(t, 5, interval)

	// Test non-existing platform - returns default minimum interval of 1 second
	interval = cfg.GetPlatformMinAccessInterval("bilibili")
	assert.Equal(t, 1, interval) // 默认最小间隔为 1 秒，防止无限制高频访问
}

func TestDefaultPlatformRateLimitSurvivesTransientConfigUpdate(t *testing.T) {
	const roomURL = "https://live.douyin.com/123456"
	limiter := ratelimit.GetGlobalRateLimiter()
	t.Cleanup(func() {
		SetCurrentConfig(NewConfig())
	})

	cfg := NewConfig()
	cfg.LiveRooms = []LiveRoom{{Url: roomURL, IsListening: true}}
	cfg.RefreshLiveRoomIndexCache()
	SetCurrentConfig(cfg)

	if got := limiter.GetAllPlatformLimits()[PlatformKeyDouyin]; got != 1 {
		t.Fatalf("初始默认平台限流 = %d 秒，期望 1 秒", got)
	}

	if _, err := SetLiveRoomId(roomURL, types.LiveID("douyin-test")); err != nil {
		t.Fatalf("写入临时 LiveId 失败: %v", err)
	}
	if got := limiter.GetAllPlatformLimits()[PlatformKeyDouyin]; got != 1 {
		t.Fatalf("临时配置更新后的默认平台限流 = %d 秒，期望仍为 1 秒", got)
	}
}

func TestBackwardsCompatibility(t *testing.T) {
	// Test that old config files still work
	oldConfigYaml := `
rpc:
  enable: true
  bind: :8080
debug: false
interval: 30
out_put_path: ./
live_rooms:
- url: https://live.bilibili.com/123456
  is_listening: true
`
	cfg, err := NewConfigWithBytes([]byte(oldConfigYaml))
	assert.NoError(t, err)
	assert.NotNil(t, cfg.PlatformConfigs)
	assert.Equal(t, 30, cfg.Interval)
	assert.Len(t, cfg.LiveRooms, 1)
	assert.Equal(t, "https://live.bilibili.com/123456", cfg.LiveRooms[0].Url)

	// Test that resolve works with no overrides
	resolved := cfg.ResolveConfigForRoom(&cfg.LiveRooms[0], "bilibili")
	assert.Equal(t, 30, resolved.Interval)
	assert.Equal(t, "./", resolved.OutPutPath)
}

func TestDanmakuFormatsMigrationAndValidation(t *testing.T) {
	withoutFormats, err := NewConfigWithBytes([]byte(`
danmaku:
  font_size: 40
`))
	assert.NoError(t, err)
	assert.Equal(t, []DanmakuFormat{DanmakuFormatASS}, withoutFormats.Danmaku.Formats)

	legacy, err := NewConfigWithBytes([]byte(`
danmaku:
  enable: true
  save_gift: false
`))
	assert.NoError(t, err)
	assert.True(t, legacy.DanmakuEnable)
	assert.Equal(t, []DanmakuFormat{DanmakuFormatXML}, legacy.Danmaku.Formats)
	assert.False(t, *legacy.Danmaku.RecordGift)

	legacyTimestampOnly, err := NewConfigWithBytes([]byte(`
danmaku:
  use_server_timestamp: false
`))
	assert.NoError(t, err)
	assert.Equal(t, []DanmakuFormat{DanmakuFormatXML}, legacyTimestampOnly.Danmaku.Formats)
	assert.False(t, legacyTimestampOnly.Danmaku.ServerTimestampEnabled())

	jsonOnly, err := NewConfigWithBytes([]byte(`
danmaku:
  formats: [json]
`))
	assert.Error(t, err)
	assert.Nil(t, jsonOnly)

	empty, err := NewConfigWithBytes([]byte(`
danmaku:
  formats: []
`))
	assert.ErrorContains(t, err, "至少选择一种")
	assert.Nil(t, empty)

	unknown, err := NewConfigWithBytes([]byte(`
danmaku:
  formats: [yaml]
`))
	assert.ErrorContains(t, err, "不支持的格式")
	assert.Nil(t, unknown)
}

func TestDanmakuConfigInheritance(t *testing.T) {
	global := GetDefaultDanmakuConfig()
	global.Formats = []DanmakuFormat{DanmakuFormatASS}
	platformFormats := []DanmakuFormat{DanmakuFormatXML}
	platformCookie := false
	platformFontSize := 48
	room := &LiveRoom{Url: "https://live.bilibili.com/123"}
	room.Danmaku = &DanmakuConfig{Formats: []DanmakuFormat{DanmakuFormatASS}}
	cfg := &Config{
		Danmaku: global,
		PlatformConfigs: map[string]PlatformConfig{
			"bilibili": {OverridableConfig: OverridableConfig{Danmaku: &DanmakuConfig{
				Formats: platformFormats, UseCookie: &platformCookie, FontSize: platformFontSize,
			}}},
		},
	}

	resolved := cfg.ResolveConfigForRoom(room, "bilibili")
	assert.Equal(t, []DanmakuFormat{DanmakuFormatASS}, resolved.Danmaku.Formats)
	assert.Equal(t, platformFontSize, resolved.Danmaku.FontSize)
	assert.NotNil(t, resolved.Danmaku.UseCookie)
	assert.False(t, *resolved.Danmaku.UseCookie)

	room.Danmaku = &DanmakuConfig{}
	resolved = cfg.ResolveConfigForRoom(room, "bilibili")
	assert.Equal(t, platformFormats, resolved.Danmaku.Formats)
}

func TestGetPlatformKeyFromUrl(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://live.bilibili.com/123456", "bilibili"},
		{"https://live.douyin.com/789", "douyin"},
		{"https://v.douyin.com/abc", "douyin"},
		{"https://www.douyu.com/room/123", "douyu"},
		{"https://play.sooplive.com/mbntv", "sooplive"},
		{"https://unknown.domain.com/room", "unknown.domain.com"},
		{"invalid-url", ""},
	}

	for _, test := range tests {
		result := GetPlatformKeyFromUrl(test.url)
		assert.Equal(t, test.expected, result, "URL: %s", test.url)
	}
}

func TestSetCookieDeletesEmptyCookie(t *testing.T) {
	cfg := NewConfig()
	cfg.Cookies = map[string]string{
		"play.sooplive.com": "SESS=abc",
	}
	SetCurrentConfig(cfg)

	newCfg, err := SetCookie("play.sooplive.com", "")
	assert.NoError(t, err)
	assert.NotNil(t, newCfg)
	_, exists := newCfg.Cookies["play.sooplive.com"]
	assert.False(t, exists)
}

func TestHierarchicalConfigFromExistingConfig(t *testing.T) {
	// 使用内联配置字符串测试层级配置功能，不依赖外部 config.yml 文件
	hierarchicalConfigYaml := `
rpc:
  enable: true
  bind: :8080
debug: false
interval: 20
out_put_path: ./
live_rooms:
- url: https://live.bilibili.com/123456
  is_listening: true
platform_configs:
  bilibili:
    interval: 30
    name: "哔哩哔哩"
    min_access_interval_sec: 1
  douyin:
    interval: 15
    name: "抖音"
`
	cfg, err := NewConfigWithBytes([]byte(hierarchicalConfigYaml))
	assert.NoError(t, err)
	assert.NotNil(t, cfg.PlatformConfigs)
	assert.Equal(t, 20, cfg.Interval) // 全局配置
	assert.Equal(t, "./", cfg.OutPutPath)

	// 验证平台配置已正确加载
	assert.Len(t, cfg.PlatformConfigs, 2)
	assert.Equal(t, 30, *cfg.PlatformConfigs["bilibili"].Interval)
	assert.Equal(t, 15, *cfg.PlatformConfigs["douyin"].Interval)

	// 测试 bilibili 平台使用平台级覆盖配置
	room := &LiveRoom{Url: "https://live.bilibili.com/123456"}
	resolved := cfg.ResolveConfigForRoom(room, "bilibili")
	assert.Equal(t, 30, resolved.Interval)     // 平台级覆盖 (bilibili 有 interval: 30)
	assert.Equal(t, "./", resolved.OutPutPath) // 使用全局设置 (无覆盖)

	// 测试 douyin 平台使用平台级覆盖配置
	roomDouyin := &LiveRoom{Url: "https://live.douyin.com/789"}
	resolvedDouyin := cfg.ResolveConfigForRoom(roomDouyin, "douyin")
	assert.Equal(t, 15, resolvedDouyin.Interval) // 平台级覆盖 (douyin 有 interval: 15)

	// 测试没有平台配置时使用全局默认值
	roomUnknown := &LiveRoom{Url: "https://unknown.platform.com/123"}
	resolvedUnknown := cfg.ResolveConfigForRoom(roomUnknown, "unknown")
	assert.Equal(t, 20, resolvedUnknown.Interval) // 使用全局默认值
}

func TestBarkConfig_Load(t *testing.T) {
	barkConfigYaml := `
rpc:
  enable: true
  bind: :8080
interval: 20
out_put_path: ./
notify:
  bark:
    enable: true
    serverURL: "https://my-bark.example.com"
    deviceKey: "test_device_key_123456"
    sound: "alarm"
    group: "bililive-go"
    icon: "https://example.com/icon.png"
    level: "timeSensitive"
`
	cfg, err := NewConfigWithBytes([]byte(barkConfigYaml))
	assert.NoError(t, err)
	assert.True(t, cfg.Notify.Bark.Enable)
	assert.Equal(t, "https://my-bark.example.com", cfg.Notify.Bark.ServerURL)
	assert.Equal(t, "test_device_key_123456", cfg.Notify.Bark.DeviceKey)
	assert.Equal(t, "alarm", cfg.Notify.Bark.Sound)
	assert.Equal(t, "bililive-go", cfg.Notify.Bark.Group)
	assert.Equal(t, "https://example.com/icon.png", cfg.Notify.Bark.Icon)
	assert.Equal(t, "timeSensitive", cfg.Notify.Bark.Level)
}

func TestBarkConfig_BackwardCompatibility(t *testing.T) {
	// 旧配置文件没有 bark 字段，应正常加载且使用默认值
	oldConfigYaml := `
rpc:
  enable: true
  bind: :8080
interval: 30
out_put_path: ./
notify:
  telegram:
    enable: false
  email:
    enable: false
`
	cfg, err := NewConfigWithBytes([]byte(oldConfigYaml))
	assert.NoError(t, err)
	assert.False(t, cfg.Notify.Bark.Enable)
	assert.Equal(t, "https://api.day.app", cfg.Notify.Bark.ServerURL)
	assert.Equal(t, "bililive-go", cfg.Notify.Bark.Group)
}

func TestBarkConfig_DefaultValues(t *testing.T) {
	cfg := NewConfig()
	assert.False(t, cfg.Notify.Bark.Enable)
	assert.Equal(t, "https://api.day.app", cfg.Notify.Bark.ServerURL)
	assert.Equal(t, "bililive-go", cfg.Notify.Bark.Group)
	assert.Equal(t, "", cfg.Notify.Bark.DeviceKey)
	assert.Equal(t, "", cfg.Notify.Bark.Sound)
	assert.Equal(t, "", cfg.Notify.Bark.Icon)
	assert.Equal(t, "", cfg.Notify.Bark.Level)
}

func TestSoopLiveAuth_LoadAndSet(t *testing.T) {
	cfgYaml := `
rpc:
  enable: true
  bind: :8080
interval: 20
out_put_path: ./
sooplive_auth:
  username: "tester"
  password: "secret"
`
	cfg, err := NewConfigWithBytes([]byte(cfgYaml))
	assert.NoError(t, err)
	assert.Equal(t, "tester", cfg.SoopLiveAuth.Username)
	assert.Equal(t, "secret", cfg.SoopLiveAuth.Password)
}

func TestBackfillLiveRoomNickName(t *testing.T) {
	const roomURL = "http://live.bilibili.com/123"

	// 每个子用例都基于临时配置文件，以便验证回填是否真正落盘。
	setup := func(t *testing.T, nickName string) string {
		t.Helper()
		configFile := filepath.Join(t.TempDir(), "config.yml")
		content := "rpc:\n  enable: true\nlive_rooms:\n  - url: " + roomURL + "\n"
		if nickName != "" {
			content += "    nick_name: " + nickName + "\n"
		}
		assert.NoError(t, os.WriteFile(configFile, []byte(content), 0644))

		cfg, err := NewConfigWithFile(configFile)
		assert.NoError(t, err)
		SetCurrentConfig(cfg)
		t.Cleanup(func() { SetCurrentConfig(NewConfig()) })
		return configFile
	}

	t.Run("空别名回填并持久化", func(t *testing.T) {
		configFile := setup(t, "")

		assert.NoError(t, BackfillLiveRoomNickName(roomURL, "原主播名"))

		room, err := GetCurrentConfig().GetLiveRoomByUrl(roomURL)
		assert.NoError(t, err)
		assert.Equal(t, "原主播名", room.NickName)

		content, err := os.ReadFile(configFile)
		assert.NoError(t, err)
		assert.Contains(t, string(content), "nick_name: 原主播名")
	})

	t.Run("非空别名不被覆盖", func(t *testing.T) {
		setup(t, "用户自定义")

		assert.NoError(t, BackfillLiveRoomNickName(roomURL, "平台新名"))

		room, err := GetCurrentConfig().GetLiveRoomByUrl(roomURL)
		assert.NoError(t, err)
		assert.Equal(t, "用户自定义", room.NickName)
	})

	t.Run("主播名为空不写入", func(t *testing.T) {
		setup(t, "")

		assert.NoError(t, BackfillLiveRoomNickName(roomURL, ""))

		room, err := GetCurrentConfig().GetLiveRoomByUrl(roomURL)
		assert.NoError(t, err)
		assert.Equal(t, "", room.NickName)
	})

	t.Run("房间不存在不报错", func(t *testing.T) {
		setup(t, "")

		assert.NoError(t, BackfillLiveRoomNickName("http://live.bilibili.com/999", "任意名称"))
	})

	// R3：手动清空别名等于「希望重新固化」，下一次取到主播名时应再次回填。
	t.Run("清空别名后可再次回填", func(t *testing.T) {
		setup(t, "")

		assert.NoError(t, BackfillLiveRoomNickName(roomURL, "首次固化"))
		room, err := GetCurrentConfig().GetLiveRoomByUrl(roomURL)
		assert.NoError(t, err)
		assert.Equal(t, "首次固化", room.NickName)

		// 模拟用户在前端手动清空别名
		_, err = UpdateWithRetry(func(c *Config) error {
			r, e := c.GetLiveRoomByUrl(roomURL)
			if e != nil {
				return e
			}
			r.NickName = ""
			return nil
		}, 3, 10*time.Millisecond)
		assert.NoError(t, err)

		assert.NoError(t, BackfillLiveRoomNickName(roomURL, "清空后新名"))
		room, err = GetCurrentConfig().GetLiveRoomByUrl(roomURL)
		assert.NoError(t, err)
		assert.Equal(t, "清空后新名", room.NickName)
	})
}

// Helper functions for pointer conversion
func intPtr(i int) *int {
	return &i
}

func stringPtr(s string) *string {
	return &s
}
