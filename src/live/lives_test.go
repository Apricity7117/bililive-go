package live

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/types"
)

type blockingInfoLive struct {
	Live
	calls        atomic.Int32
	firstStarted chan struct{}
	releaseFirst chan struct{}
}

func (l *blockingInfoLive) GetRawUrl() string { return "" }

func (l *blockingInfoLive) GetInfo() (*Info, error) {
	if l.calls.Add(1) == 1 {
		close(l.firstStarted)
		<-l.releaseFirst
	}
	return &Info{}, nil
}

type errorInfoLive struct{ Live }

func (l *errorInfoLive) GetRawUrl() string         { return "" }
func (l *errorInfoLive) GetLiveId() types.LiveID   { return "error-test" }
func (l *errorInfoLive) GetPlatformCNName() string { return "测试平台" }
func (l *errorInfoLive) GetInfo() (*Info, error)   { return nil, errors.New("请求失败") }

// 未加载全局配置时 getConfiguredInterval 会直接返回默认间隔，
// 因此可以直接构造一个空的 WrappedLive 来验证退避曲线。
func TestNextRequestIntervalLocked(t *testing.T) {
	tests := []struct {
		name     string
		failures int
		want     time.Duration
	}{
		{name: "无失败时使用配置间隔", failures: 0, want: defaultInterval},
		{name: "一次失败翻倍", failures: 1, want: 2 * defaultInterval},
		{name: "两次失败四倍", failures: 2, want: 4 * defaultInterval},
		{name: "多次失败后封顶", failures: 100, want: maxFailureBackoff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := &WrappedLive{consecutiveFailures: tt.failures}
			w.mu.Lock()
			got := w.nextRequestIntervalLocked()
			w.mu.Unlock()
			if got != tt.want {
				t.Errorf("consecutiveFailures=%d: got %v, want %v", tt.failures, got, tt.want)
			}
		})
	}
}

// 退避间隔必须单调不减，且永远不会超过上限，
// 否则失败时的请求频率反而会高于正常轮询频率。
func TestNextRequestIntervalNeverShrinksBelowConfigured(t *testing.T) {
	prev := time.Duration(0)
	for failures := 0; failures < 64; failures++ {
		w := &WrappedLive{consecutiveFailures: failures}
		w.mu.Lock()
		got := w.nextRequestIntervalLocked()
		w.mu.Unlock()
		if got < defaultInterval {
			t.Fatalf("consecutiveFailures=%d: 退避间隔 %v 小于配置间隔 %v", failures, got, defaultInterval)
		}
		if got > maxFailureBackoff {
			t.Fatalf("consecutiveFailures=%d: 退避间隔 %v 超过上限 %v", failures, got, maxFailureBackoff)
		}
		if got < prev {
			t.Fatalf("consecutiveFailures=%d: 退避间隔 %v 小于上一次的 %v", failures, got, prev)
		}
		prev = got
	}
}

func TestFailureBackoffDoesNotShortenLongConfiguredInterval(t *testing.T) {
	configured := 10 * time.Minute
	for _, failures := range []int{0, 1, 100} {
		if got := failureBackoffInterval(configured, failures); got < configured {
			t.Fatalf("consecutiveFailures=%d: 退避间隔 %v 小于配置间隔 %v", failures, got, configured)
		}
	}
}

func TestInitializingFailureCountsTowardBackoff(t *testing.T) {
	w := &WrappedLive{}
	placeholder := &Info{Initializing: true, LastError: "平台请求失败"}

	if failed := w.recordRequestResult(placeholder, nil); !failed {
		t.Fatal("初始化占位信息携带 LastError 时应计为失败")
	}
	w.mu.Lock()
	failures := w.consecutiveFailures
	w.mu.Unlock()
	if failures != 1 {
		t.Fatalf("连续失败次数 = %d，期望 1", failures)
	}

	if failed := w.recordRequestResult(&Info{}, nil); failed {
		t.Fatal("正常信息不应计为失败")
	}
	w.mu.Lock()
	failures = w.consecutiveFailures
	w.mu.Unlock()
	if failures != 0 {
		t.Fatalf("成功后连续失败次数 = %d，期望清零", failures)
	}
}

func TestWrappedLiveSerializesConcurrentGetInfo(t *testing.T) {
	underlying := &blockingInfoLive{
		firstStarted: make(chan struct{}),
		releaseFirst: make(chan struct{}),
	}
	w := NewWrappedLive(context.Background(), underlying, nil).(*WrappedLive)

	done := make(chan struct{}, 2)
	go func() {
		_, _ = w.GetInfo()
		done <- struct{}{}
	}()
	<-underlying.firstStarted
	go func() {
		_, _ = w.GetInfo()
		done <- struct{}{}
	}()

	time.Sleep(50 * time.Millisecond)
	if calls := underlying.calls.Load(); calls != 1 {
		t.Fatalf("首个请求完成前底层 GetInfo 被调用 %d 次，期望 1 次", calls)
	}

	close(underlying.releaseFirst)
	for range 2 {
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("并发 GetInfo 未完成")
		}
	}
}

func TestRequestStatusCallbackHandlesNilInfoOnFailure(t *testing.T) {
	callbackCalled := false
	SetRequestStatusCallback(func(_, _ string, success bool, errMsg string) {
		callbackCalled = true
		if success || errMsg != "请求失败" {
			t.Fatalf("错误的请求状态回调：success=%v, err=%q", success, errMsg)
		}
	})
	t.Cleanup(func() { SetRequestStatusCallback(nil) })

	w := NewWrappedLive(context.Background(), &errorInfoLive{}, nil).(*WrappedLive)
	if _, err := w.GetInfo(); err == nil {
		t.Fatal("底层请求失败时应返回错误")
	}
	if !callbackCalled {
		t.Fatal("请求失败时未调用状态回调")
	}
}

// nickNameLive 是带内存选项的假 Live，用于验证别名固化与内存快照同步。
// SetNickName 采用与 BaseLive 相同的换指针写法，避免并发读 Options 时发生数据竞争。
type nickNameLive struct {
	Live
	rawUrl string
	info   *Info
	err    error
	opts   *Options
	logger *livelogger.LiveLogger
}

func (l *nickNameLive) GetRawUrl() string                 { return l.rawUrl }
func (l *nickNameLive) GetLiveId() types.LiveID           { return "nickname-test" }
func (l *nickNameLive) GetPlatformCNName() string         { return "测试平台" }
func (l *nickNameLive) GetOptions() *Options              { return l.opts }
func (l *nickNameLive) GetLogger() *livelogger.LiveLogger { return l.logger }
func (l *nickNameLive) GetInfo() (*Info, error)           { return l.info, l.err }

func (l *nickNameLive) SetNickName(nickName string) {
	cp := *l.opts
	cp.NickName = nickName
	l.opts = &cp
}

// setupNickNameConfig 载入只含一个直播间的临时配置，返回该直播间 URL。
func setupNickNameConfig(t *testing.T, nickName string) string {
	t.Helper()
	const roomURL = "http://live.bilibili.com/2233"
	configFile := filepath.Join(t.TempDir(), "config.yml")
	content := "rpc:\n  enable: true\nlive_rooms:\n  - url: " + roomURL + "\n"
	if nickName != "" {
		content += "    nick_name: " + nickName + "\n"
	}
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatalf("写入临时配置失败：%v", err)
	}
	cfg, err := configs.NewConfigWithFile(configFile)
	if err != nil {
		t.Fatalf("加载临时配置失败：%v", err)
	}
	configs.SetCurrentConfig(cfg)
	t.Cleanup(func() { configs.SetCurrentConfig(configs.NewConfig()) })
	return roomURL
}

func newNickNameLive(rawUrl string, info *Info, err error) *nickNameLive {
	return &nickNameLive{
		rawUrl: rawUrl,
		info:   info,
		err:    err,
		opts:   MustNewOptions(),
		logger: livelogger.New(1024, nil),
	}
}

func currentNickName(t *testing.T, rawUrl string) string {
	t.Helper()
	room, err := configs.GetCurrentConfig().GetLiveRoomByUrl(rawUrl)
	if err != nil {
		t.Fatalf("配置中找不到直播间 %s：%v", rawUrl, err)
	}
	return room.NickName
}

// 成功拿到主播名时既要落盘，也要同步内存选项，否则录制模板取到的仍是旧别名。
func TestReconcileNickNameBackfillsAndSyncsOptions(t *testing.T) {
	roomURL := setupNickNameConfig(t, "")
	l := newNickNameLive(roomURL, nil, nil)

	ReconcileNickName(l, "平台主播名")

	if got := currentNickName(t, roomURL); got != "平台主播名" {
		t.Fatalf("配置中的别名 = %q，期望 %q", got, "平台主播名")
	}
	if got := l.GetOptions().NickName; got != "平台主播名" {
		t.Fatalf("内存选项中的别名 = %q，期望 %q", got, "平台主播名")
	}
}

// 主播名为空（平台未返回）时不能把别名写空，也不该改动内存快照。
func TestReconcileNickNameSkipsEmptyHostName(t *testing.T) {
	roomURL := setupNickNameConfig(t, "")
	l := newNickNameLive(roomURL, nil, nil)

	ReconcileNickName(l, "")

	if got := currentNickName(t, roomURL); got != "" {
		t.Fatalf("主播名为空时不应回填，实际别名 = %q", got)
	}
	if got := l.GetOptions().NickName; got != "" {
		t.Fatalf("主播名为空时不应改动内存选项，实际 = %q", got)
	}
}

// 用户手动设置过的别名永不被平台主播名覆盖。
func TestReconcileNickNameKeepsUserNickName(t *testing.T) {
	roomURL := setupNickNameConfig(t, "用户自定义")
	l := newNickNameLive(roomURL, nil, nil)

	ReconcileNickName(l, "平台新名")

	if got := currentNickName(t, roomURL); got != "用户自定义" {
		t.Fatalf("配置中的别名 = %q，期望保持 %q", got, "用户自定义")
	}
	if got := l.GetOptions().NickName; got != "用户自定义" {
		t.Fatalf("内存选项应同步为配置值 %q，实际 = %q", "用户自定义", got)
	}
}

// 请求失败（InitializingLive 占位信息）时不能拿占位主播名去固化别名。
func TestGetInfoDoesNotBackfillOnRequestFailure(t *testing.T) {
	roomURL := setupNickNameConfig(t, "")
	placeholder := &Info{HostName: "初始化中...", Initializing: true, LastError: "平台请求失败"}
	underlying := newNickNameLive(roomURL, placeholder, nil)
	placeholder.Live = underlying
	w := NewWrappedLive(context.Background(), underlying, nil).(*WrappedLive)

	if _, err := w.GetInfo(); err != nil {
		t.Fatalf("占位信息不应返回错误：%v", err)
	}
	if got := currentNickName(t, roomURL); got != "" {
		t.Fatalf("请求失败时不应回填别名，实际 = %q", got)
	}
}

// 请求成功时应经由 WrappedLive 透传到底层 Live 的别名写入方法。
func TestGetInfoBackfillsThroughWrappedLive(t *testing.T) {
	roomURL := setupNickNameConfig(t, "")
	underlying := newNickNameLive(roomURL, &Info{HostName: "真实主播名"}, nil)
	underlying.info.Live = underlying
	w := NewWrappedLive(context.Background(), underlying, nil).(*WrappedLive)

	if _, err := w.GetInfo(); err != nil {
		t.Fatalf("GetInfo 不应失败：%v", err)
	}
	if got := currentNickName(t, roomURL); got != "真实主播名" {
		t.Fatalf("配置中的别名 = %q，期望 %q", got, "真实主播名")
	}
	if got := underlying.GetOptions().NickName; got != "真实主播名" {
		t.Fatalf("内存选项中的别名 = %q，期望 %q", got, "真实主播名")
	}
}

// resolveNickName 以配置为准；房间不在配置中时才回退到内存选项快照。
func TestResolveNickName(t *testing.T) {
	roomURL := setupNickNameConfig(t, "配置中的别名")

	t.Run("配置中有房间时优先配置值", func(t *testing.T) {
		l := newNickNameLive(roomURL, nil, nil)
		l.opts.NickName = "过时的快照"
		info := &Info{Live: l}
		if got := info.resolveNickName(); got != "配置中的别名" {
			t.Fatalf("别名 = %q，期望 %q", got, "配置中的别名")
		}
	})

	t.Run("房间不在配置中时回退选项快照", func(t *testing.T) {
		l := newNickNameLive("http://live.bilibili.com/9999", nil, nil)
		l.opts.NickName = "选项快照"
		info := &Info{Live: l}
		if got := info.resolveNickName(); got != "选项快照" {
			t.Fatalf("别名 = %q，期望 %q", got, "选项快照")
		}
	})
}
