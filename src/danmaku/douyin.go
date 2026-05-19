package danmaku

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/pkg/utils"
	remotetools "github.com/kira1928/remotetools/pkg/tools"
)

const (
	douyinBToolsPort      = 18110
	douyinBToolsAuthToken = "Basic YTph"
	douyinRetryInterval   = 2 * time.Second
)

//go:embed douyin_bridge.cjs
var douyinBridgeScript string

type douyinClient struct {
	liveObj live.Live
	roomID  string
	logger  *livelogger.LiveLogger
	cfg     configs.DanmakuRecord

	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

type douyinRuntime struct {
	nodePath   string
	btoolsDir  string
	bundlePath string
}

func newDouyinClient(liveObj live.Live, roomID string, logger *livelogger.LiveLogger, cfg configs.DanmakuRecord) *douyinClient {
	return &douyinClient{
		liveObj: liveObj,
		roomID:  strings.Trim(roomID, "/ "),
		logger:  logger,
		cfg:     cfg,
	}
}

func (c *douyinClient) Listen(ctx context.Context, onMessage func(Message)) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.listenOnce(ctx, onMessage); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.WithError(err).Warn("抖音弹幕连接异常，2 秒后重试")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(douyinRetryInterval):
			}
			continue
		}
		return nil
	}
}

func (c *douyinClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stdin == nil {
		return nil
	}
	_, _ = io.WriteString(c.stdin, "close\n")
	err := c.stdin.Close()
	c.stdin = nil
	return err
}

func (c *douyinClient) listenOnce(ctx context.Context, onMessage func(Message)) error {
	roomID, err := c.resolveRoomID(ctx)
	if err != nil {
		return err
	}
	if roomID != "" {
		c.roomID = roomID
	}

	runtime, err := resolveDouyinRuntime()
	if err != nil {
		return err
	}
	bridgePath, err := prepareDouyinBridgeScript()
	if err != nil {
		return err
	}

	cmd := exec.Command(runtime.nodePath, bridgePath)
	cmd.Dir = runtime.btoolsDir
	cmd.Env = append(os.Environ(),
		"BILILIVE_DOUYIN_BUNDLE="+runtime.bundlePath,
		"BILILIVE_DOUYIN_ROOM_ID="+c.roomID,
		"BILILIVE_DOUYIN_COOKIE="+c.cookieHeader(),
		"BILILIVE_DOUYIN_USE_SERVER_TIMESTAMP="+boolEnv(c.cfg.UseServerTimestamp),
		"BILILIVE_DOUYIN_SAVE_GIFT="+boolEnv(c.cfg.SaveGift),
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	c.setProcess(cmd, stdin)
	defer c.clearProcess(cmd)
	done := make(chan struct{})
	defer close(done)
	go c.logBridgeStderr(stderr)
	go c.closeWhenContextDone(ctx, done, cmd)

	if err := c.scanMessages(stdout, onMessage); err != nil {
		_ = c.Close()
		_ = cmd.Wait()
		return err
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil
	}
	if waitErr != nil {
		return fmt.Errorf("抖音弹幕桥接进程退出: %w", waitErr)
	}
	return nil
}

func (c *douyinClient) setProcess(cmd *exec.Cmd, stdin io.WriteCloser) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cmd = cmd
	c.stdin = stdin
}

func (c *douyinClient) clearProcess(cmd *exec.Cmd) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cmd == cmd {
		c.cmd = nil
		c.stdin = nil
	}
}

func (c *douyinClient) closeWhenContextDone(ctx context.Context, done <-chan struct{}, cmd *exec.Cmd) {
	select {
	case <-ctx.Done():
	case <-done:
		return
	}
	_ = c.Close()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case <-done:
		return
	case <-timer.C:
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}
}

func (c *douyinClient) scanMessages(reader io.Reader, onMessage func(Message)) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var message Message
		if err := json.Unmarshal(line, &message); err != nil {
			c.logger.WithError(err).Debugf("解析抖音弹幕桥接输出失败: %s", string(line))
			continue
		}
		if message.Type == "" {
			continue
		}
		onMessage(message)
	}
	return scanner.Err()
}

func (c *douyinClient) logBridgeStderr(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 16*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event struct {
			Level   string `json:"level"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(line), &event); err == nil && event.Message != "" {
			switch event.Level {
			case "error", "warn":
				c.logger.Warn(event.Message)
			case "info":
				c.logger.Info(event.Message)
			default:
				c.logger.Debug(event.Message)
			}
			continue
		}
		c.logger.Debug(line)
	}
}

func (c *douyinClient) resolveRoomID(ctx context.Context) (string, error) {
	roomID := strings.TrimSpace(c.roomID)
	reqURL := fmt.Sprintf("http://127.0.0.1:%d/bgo/channel-info?url=%s", douyinBToolsPort, url.QueryEscape(c.liveObj.GetRawUrl()))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", douyinBToolsAuthToken)
	resp, err := utils.CreateDefaultClient().Do(req)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var decoded struct {
				ID string `json:"id"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&decoded); err == nil && strings.TrimSpace(decoded.ID) != "" {
				roomID = strings.TrimSpace(decoded.ID)
			}
		}
	}
	if liveID := c.resolveLiveID(ctx, roomID); liveID != "" {
		return liveID, nil
	}
	if roomID != "" && roomID != "." {
		return roomID, nil
	}
	return "", fmt.Errorf("抖音房间号为空")
}

func (c *douyinClient) resolveLiveID(ctx context.Context, roomID string) string {
	if roomID == "" || roomID == "." {
		return ""
	}
	reqURL := fmt.Sprintf(
		"http://127.0.0.1:%d/bgo/live-info?platform=douyin&roomId=%s&dev=1",
		douyinBToolsPort,
		url.QueryEscape(roomID),
	)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", douyinBToolsAuthToken)
	resp, err := utils.CreateDefaultClient().Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var decoded struct {
		LiveID string `json:"liveId"`
		Dev    struct {
			LiveID string `json:"liveId"`
			RoomID string `json:"roomId"`
		} `json:"dev"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return ""
	}
	if liveID := strings.TrimSpace(decoded.Dev.LiveID); liveID != "" {
		return liveID
	}
	return strings.TrimSpace(decoded.LiveID)
}

func (c *douyinClient) cookieHeader() string {
	if !c.cfg.UseCookie {
		return ""
	}
	options := c.liveObj.GetOptions()
	if options == nil || options.Cookies == nil {
		return ""
	}
	parsed, err := url.Parse(c.liveObj.GetRawUrl())
	if err != nil {
		return ""
	}
	cookies := options.Cookies.Cookies(parsed)
	if parsed.Host != "live.douyin.com" {
		if liveURL, err := url.Parse("https://live.douyin.com/"); err == nil {
			cookies = append(cookies, options.Cookies.Cookies(liveURL)...)
		}
	}
	parts := make([]string, 0, len(cookies))
	seen := map[string]struct{}{}
	for _, cookie := range cookies {
		if _, ok := seen[cookie.Name]; ok {
			continue
		}
		seen[cookie.Name] = struct{}{}
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func resolveDouyinRuntime() (douyinRuntime, error) {
	if bundlePath := strings.TrimSpace(os.Getenv("BILILIVE_DOUYIN_BUNDLE")); bundlePath != "" {
		nodePath := strings.TrimSpace(os.Getenv("BILILIVE_DOUYIN_NODE"))
		if nodePath == "" {
			var err error
			nodePath, err = exec.LookPath("node")
			if err != nil {
				return douyinRuntime{}, fmt.Errorf("未找到 node: %w", err)
			}
		}
		return douyinRuntime{
			nodePath:   nodePath,
			btoolsDir:  filepath.Dir(bundlePath),
			bundlePath: bundlePath,
		}, nil
	}

	api := remotetools.Get()
	if api == nil {
		return douyinRuntime{}, fmt.Errorf("remotetools 尚未初始化，无法定位抖音弹幕运行环境")
	}
	node, err := api.GetTool("node")
	if err != nil {
		return douyinRuntime{}, err
	}
	if !node.DoesToolExist() {
		return douyinRuntime{}, fmt.Errorf("node 工具尚未安装，无法录制抖音弹幕")
	}
	btools, err := api.GetTool("biliLive-tools")
	if err != nil {
		return douyinRuntime{}, err
	}
	if !btools.DoesToolExist() {
		return douyinRuntime{}, fmt.Errorf("biliLive-tools 工具尚未安装，无法录制抖音弹幕")
	}
	nodePath, err := filepath.Abs(node.GetToolPath())
	if err != nil {
		return douyinRuntime{}, err
	}
	btoolsPath, err := filepath.Abs(btools.GetToolPath())
	if err != nil {
		return douyinRuntime{}, err
	}
	btoolsDir := filepath.Dir(btoolsPath)
	bundlePath, err := findDouyinBundle(btoolsDir)
	if err != nil {
		return douyinRuntime{}, err
	}
	return douyinRuntime{
		nodePath:   nodePath,
		btoolsDir:  btoolsDir,
		bundlePath: bundlePath,
	}, nil
}

func findDouyinBundle(dir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.cjs"))
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, file := range files {
		data, err := os.ReadFile(file)
		if err != nil {
			continue
		}
		if bytes.Contains(data, []byte("class DouYinDanmaClient")) &&
			bytes.Contains(data, []byte("WebcastChatMessage")) {
			return file, nil
		}
	}
	return "", fmt.Errorf("未在 %s 找到抖音弹幕客户端", dir)
}

func prepareDouyinBridgeScript() (string, error) {
	sum := sha1.Sum([]byte(douyinBridgeScript))
	path := filepath.Join(os.TempDir(), "bililive-go-douyin-bridge-"+hex.EncodeToString(sum[:8])+".cjs")
	if data, err := os.ReadFile(path); err == nil && string(data) == douyinBridgeScript {
		return path, nil
	}
	if err := os.WriteFile(path, []byte(douyinBridgeScript), 0o600); err != nil {
		return "", err
	}
	return path, nil
}

func boolEnv(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
