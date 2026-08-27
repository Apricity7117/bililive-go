package danmaku

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/bilibili"
)

// DanmakuRecorder 哔哩哔哩弹幕录制器
type DanmakuRecorder struct {
	baseRecorder
	roomID  int
	cookies string
	client  *bilibili.Client
}

// NewDanmakuRecorder 创建哔哩哔哩弹幕录制器
func NewDanmakuRecorder(roomID int, cookies string, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DanmakuRecorder {
	cfg.SetDefaultsWithPlatform("bilibili")
	return &DanmakuRecorder{
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
func (d *DanmakuRecorder) Start(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.running {
		return nil
	}

	d.startAt = time.Now()

	if err := d.initWriters(d.startAt, "哔哩哔哩", strconv.Itoa(d.roomID), "Bilibili Danmaku"); err != nil {
		return fmt.Errorf("failed to create danmaku writers: %w", err)
	}

	cookies := d.cookies
	if !d.cfg.CookieEnabled() {
		cookies = ""
	}
	c := bilibili.NewClient(d.roomID, cookies, d.logger)

	c.OnDanmaku(func(msg bilibili.DanmakuMsg) {
		recvAt := time.Now()
		d.addEvent(Event{Type: EventComment, ReceivedAt: recvAt, EventTimestamp: msg.Timestamp, Text: msg.Content, Color: msg.Color, Mode: msg.Mode, UID: strconv.FormatInt(msg.UID, 10), Username: msg.Uname}, "danmaku", map[string]interface{}{
			"color": msg.Color, "timestamp": recvAt.Unix(), "uid": msg.UID,
		})
	})

	if d.cfg.RecordGift != nil && *d.cfg.RecordGift {
		c.OnGift(func(msg bilibili.GiftMsg) {
			if msg.Num > 0 {
				recvAt := time.Now()
				priceMilli := int64(0)
				if msg.CoinType == "gold" {
					priceMilli = int64(msg.Price)
				}
				d.addEvent(Event{Type: EventGift, ReceivedAt: recvAt, EventTimestamp: msg.Timestamp, Name: msg.GiftName, Count: int64(msg.Num), PriceMilli: priceMilli, CoinType: msg.CoinType, UID: strconv.FormatInt(msg.UID, 10), Username: msg.Uname}, "gift", map[string]interface{}{
					"gift_name": msg.GiftName, "num": msg.Num, "price": msg.Price, "coin_type": msg.CoinType, "timestamp": recvAt.Unix(), "uid": msg.UID,
				})
			}
		})
	}

	if d.cfg.RecordGuard != nil && *d.cfg.RecordGuard {
		c.OnGuardBuy(func(msg bilibili.GuardBuyMsg) {
			recvAt := time.Now()
			d.addEvent(Event{Type: EventGuard, ReceivedAt: recvAt, EventTimestamp: msg.Timestamp, Name: msg.GiftName, Count: int64(msg.Num), PriceMilli: int64(msg.Price), Level: msg.GuardLevel, UID: strconv.FormatInt(msg.UID, 10), Username: msg.Username}, "guard", map[string]interface{}{
				"gift_name": msg.GiftName, "price": msg.Price, "timestamp": recvAt.Unix(), "uid": msg.UID,
			})
		})
	}

	if d.cfg.RecordSuperChat != nil && *d.cfg.RecordSuperChat {
		c.OnSuperChat(func(msg bilibili.SuperChatMsg) {
			recvAt := time.Now()
			d.addEvent(Event{Type: EventSuperChat, ReceivedAt: recvAt, EventTimestamp: msg.Timestamp, Text: msg.Message, PriceMilli: int64(msg.Price) * 1000, Duration: msg.Duration, UID: strconv.FormatInt(msg.UID, 10), Username: msg.Uname}, "super_chat", map[string]interface{}{
				"price": msg.Price, "timestamp": recvAt.Unix(), "uid": msg.UID,
			})
		})
	}

	if err := c.Start(); err != nil {
		d.discardWritersLocked()
		return fmt.Errorf("failed to start bilibili danmaku client: %w", err)
	}

	d.client = c
	d.running = true
	d.logger.Info("弹幕录制已启动")

	go func() {
		<-ctx.Done()
		d.Stop()
	}()

	return nil
}

// Stop 停止弹幕录制
func (d *DanmakuRecorder) Stop() {
	w := d.stopBase()
	if w == nil {
		return
	}
	c := d.client
	d.client = nil

	if c != nil {
		c.Stop()
	}
	d.closeWriters(w)
	count := d.GetCount()
	files := d.OutputFiles()
	if count > 0 {
		d.logger.Infof("弹幕录制已停止，共录制 %d 条弹幕，输出文件: %v", count, files)
	} else {
		d.logger.Infof("弹幕录制已停止，未收到弹幕，输出文件: %v", files)
	}
}
