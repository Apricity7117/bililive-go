package danmaku

import (
	"context"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/douyu"
)

// DouyuDanmakuRecorder 斗鱼弹幕录制器
type DouyuDanmakuRecorder struct {
	baseRecorder
	roomID  string
	cookies string
	client  *douyu.DouyuClient
}

// NewDouyuDanmakuRecorder 创建斗鱼弹幕录制器
func NewDouyuDanmakuRecorder(roomID, cookies, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DouyuDanmakuRecorder {
	cfg.SetDefaultsWithPlatform("douyu")
	return &DouyuDanmakuRecorder{
		baseRecorder: baseRecorder{
			outputFile: outputFile,
			cfg:        cfg,
			logger:     logger,
		},
		roomID:  roomID,
		cookies: cookies,
	}
}

// Start 开始弹幕录制
func (r *DouyuDanmakuRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return nil
	}

	startAt := time.Now()

	if err := r.initWriters(startAt, "斗鱼", r.roomID, "Douyu Danmaku"); err != nil {
		return err
	}
	r.startAt = startAt

	cookies := r.cookies
	if !r.cfg.CookieEnabled() {
		cookies = ""
	}
	r.client = douyu.NewDouyuClient(r.roomID, cookies, nil, nil, r.logger)
	r.client.OnDanmakuEvent(func(msg douyu.DanmakuEvent) {
		recvAt := time.Now()
		r.addEvent(Event{Type: EventComment, ReceivedAt: recvAt, EventTimestamp: msg.Timestamp, Text: msg.Content, Color: msg.Color, Mode: 1, UID: msg.UID, Username: msg.Username}, "danmaku", map[string]interface{}{
			"color": msg.Color, "timestamp": recvAt.Unix(), "uid": msg.UID,
		})
	})
	if r.cfg.RecordDouyuGift == nil || *r.cfg.RecordDouyuGift {
		r.client.OnGiftEvent(func(msg douyu.GiftEvent) {
			recvAt := time.Now()
			r.addEvent(Event{Type: EventGift, ReceivedAt: recvAt, EventTimestamp: msg.Timestamp, Name: msg.GiftName, Count: int64(msg.Count), UID: msg.UID, Username: msg.Username}, "gift", map[string]interface{}{
				"gift_name": msg.GiftName, "num": msg.Count, "timestamp": recvAt.Unix(), "uid": msg.UID,
			})
		})
	}

	if err := r.client.Start(ctx); err != nil {
		r.discardWritersLocked()
		return err
	}

	r.running = true
	r.logger.Info("斗鱼弹幕录制已启动")

	return nil
}

// Stop 停止弹幕录制
func (r *DouyuDanmakuRecorder) Stop() {
	w := r.stopBase()
	c := r.client
	r.client = nil
	if c != nil {
		c.Stop()
	}
	r.closeWriters(w)
	r.logger.Infof("斗鱼弹幕录制已停止，共录制 %d 条弹幕，输出文件: %v", r.GetCount(), r.OutputFiles())
}
