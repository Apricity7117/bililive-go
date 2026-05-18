package danmaku

import (
	"context"
	"errors"
	"net/url"
	"path/filepath"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
)

var errUnsupportedPlatform = errors.New("当前平台暂不支持弹幕录制")

// Recorder 管理一次视频分段对应的弹幕连接和文件写入。
type Recorder struct {
	live    live.Live
	info    *live.Info
	cfg     configs.DanmakuRecord
	logger  *livelogger.LiveLogger
	writers []streamWriter
	client  messageClient

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	once   sync.Once
}

type messageClient interface {
	Listen(context.Context, func(Message)) error
	Close() error
}

// NewRecorder 创建弹幕录制器；未启用或格式无效时返回 nil。
func NewRecorder(
	parent context.Context,
	liveObj live.Live,
	info *live.Info,
	videoPath string,
	cfg configs.DanmakuRecord,
	logger *livelogger.LiveLogger,
) (*Recorder, error) {
	if !cfg.Enable {
		return nil, nil
	}
	formats := cfg.NormalizeFormats()
	if len(formats) == 0 {
		return nil, nil
	}

	metadata := buildMetadata(liveObj, info)
	client, err := newMessageClient(liveObj, metadata.RoomID, logger, cfg)
	if err != nil {
		if errors.Is(err, errUnsupportedPlatform) {
			logger.Warn("当前平台暂不支持弹幕录制，已跳过")
			return nil, nil
		}
		return nil, err
	}
	writers, err := newStreamWriters(videoPath, formats, metadata)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(parent)
	return &Recorder{
		live:    liveObj,
		info:    info,
		cfg:     cfg,
		logger:  logger,
		writers: writers,
		client:  client,
		ctx:     ctx,
		cancel:  cancel,
	}, nil
}

func (r *Recorder) Start() {
	if r == nil {
		return
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.logger.Infof("弹幕录制已启动: %v", r.OutputFiles())
		err := r.client.Listen(r.ctx, r.writeMessage)
		if err != nil && r.ctx.Err() == nil {
			r.logger.WithError(err).Warn("弹幕录制连接已断开")
		}
	}()
}

func (r *Recorder) Close() {
	if r == nil {
		return
	}
	r.once.Do(func() {
		r.cancel()
		_ = r.client.Close()
		r.wg.Wait()
		for _, writer := range r.writers {
			if err := writer.Close(); err != nil {
				r.logger.WithError(err).Warnf("关闭弹幕文件失败: %s", writer.Path())
			}
		}
		r.logger.Infof("弹幕录制已停止: %v", r.OutputFiles())
	})
}

func (r *Recorder) OutputFiles() []string {
	if r == nil {
		return nil
	}
	files := make([]string, 0, len(r.writers))
	for _, writer := range r.writers {
		files = append(files, writer.Path())
	}
	return files
}

func (r *Recorder) writeMessage(message Message) {
	if !r.shouldWriteMessage(message) {
		return
	}
	for _, writer := range r.writers {
		if err := writer.AddMessage(message); err != nil {
			r.logger.WithError(err).Warnf("写入弹幕文件失败: %s", writer.Path())
		}
	}
}

func (r *Recorder) shouldWriteMessage(message Message) bool {
	if r == nil {
		return false
	}
	if !r.cfg.SaveGift && (message.Type == MessageTypeGift || message.Type == MessageTypeGuard) {
		return false
	}
	return true
}

func buildMetadata(liveObj live.Live, info *live.Info) Metadata {
	roomID := ""
	if parsed, err := url.Parse(liveObj.GetRawUrl()); err == nil {
		roomID = filepath.Base(parsed.Path)
	}
	metadata := Metadata{
		Platform:             liveObj.GetPlatformCNName(),
		RoomID:               roomID,
		RecordStartTimestamp: time.Now().UnixMilli(),
		CreatedAt:            time.Now(),
	}
	if info != nil {
		metadata.RoomTitle = info.RoomName
		metadata.UserName = info.HostName
	}
	if start := liveObj.GetLastStartTime(); !start.IsZero() {
		metadata.LiveStartTimestamp = start.UnixMilli()
	}
	return metadata
}

func newMessageClient(liveObj live.Live, roomID string, logger *livelogger.LiveLogger, cfg configs.DanmakuRecord) (messageClient, error) {
	parsed, err := url.Parse(liveObj.GetRawUrl())
	if err != nil {
		return nil, err
	}
	switch parsed.Host {
	case "live.bilibili.com":
		return newBilibiliClient(liveObj, roomID, logger, cfg.UseServerTimestamp), nil
	case "live.douyin.com", "v.douyin.com", "www.douyin.com":
		return newDouyinClient(liveObj, roomID, logger, cfg), nil
	default:
		return nil, errUnsupportedPlatform
	}
}
