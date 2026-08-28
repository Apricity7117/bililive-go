package danmaku

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/bililive-go/bililive-go/src/configs"
	"github.com/bililive-go/bililive-go/src/recorders/danmaku/douyin"
)

// DouyinDanmakuRecorder 抖音弹幕录制器
type DouyinDanmakuRecorder struct {
	baseRecorder
	roomID  string
	cookies string
	client  *douyin.DouyinClient

	comboMu      sync.Mutex
	pendingCombo map[string]*pendingDouyinGift
}

const douyinComboWindow = 500 * time.Millisecond

type pendingDouyinGift struct {
	event Event
	extra map[string]interface{}
	timer *time.Timer
}

// NewDouyinDanmakuRecorder 创建抖音弹幕录制器
func NewDouyinDanmakuRecorder(roomID, cookies, outputFile string, cfg configs.DanmakuConfig, logger *logrus.Entry) *DouyinDanmakuRecorder {
	cfg.SetDefaultsWithPlatform("douyin")
	return &DouyinDanmakuRecorder{
		baseRecorder: baseRecorder{
			outputFile: outputFile,
			cfg:        cfg,
			logger:     logger,
		},
		roomID:       roomID,
		cookies:      cookies,
		pendingCombo: make(map[string]*pendingDouyinGift),
	}
}

// Start 开始弹幕录制
func (r *DouyinDanmakuRecorder) Start(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.running {
		return nil
	}

	r.startAt = time.Now()

	if err := r.initWriters(r.startAt, "抖音", r.roomID, "Douyin Danmaku"); err != nil {
		return err
	}

	// 使用平台原始消息回调保留 UID、事件时间和礼物价格；旧签名仍由客户端保留以兼容外部调用。
	cookies := r.cookies
	if !r.cfg.CookieEnabled() {
		cookies = ""
	}
	r.client = douyin.NewDouyinClient(r.roomID, cookies, nil, nil, r.logger)
	r.client.OnDanmakuEvent(func(msg douyin.ChatMessage) {
		recvAt := time.Now()
		var timestamp int64
		var uid string
		var username string
		if msg.Common != nil {
			timestamp = msg.Common.CreateTime
		}
		if msg.User != nil {
			uid = fmt.Sprintf("%d", msg.User.Id)
			username = msg.User.Nickname
		}
		if username == "" {
			username = "未知用户"
		}
		r.addEvent(Event{Type: EventComment, ReceivedAt: recvAt, EventTimestamp: timestamp, Text: msg.Content, Color: 16777215, Mode: 1, UID: uid, Username: username}, "danmaku", map[string]interface{}{
			"color": 16777215, "timestamp": recvAt.Unix(), "uid": uid,
		})
	})
	if r.cfg.RecordDouyinGift == nil || *r.cfg.RecordDouyinGift {
		r.client.OnGiftEvent(func(msg douyin.GiftMessage) {
			recvAt := time.Now()
			var timestamp int64
			var uid, username, giftName string
			if msg.Common != nil {
				timestamp = msg.Common.CreateTime
			}
			if msg.User != nil {
				uid = fmt.Sprintf("%d", msg.User.Id)
				username = msg.User.Nickname
			}
			if msg.Gift != nil {
				giftName = msg.Gift.Name
			}
			if username == "" {
				username = "未知用户"
			}
			if giftName == "" {
				giftName = "礼物"
			}
			// comboCount 是平台对连击组合的累计数量，优先使用它，避免逐条 repeatCount 重复累计。
			count := int64(msg.ComboCount)
			if count < 1 {
				count = int64(msg.RepeatCount)
			}
			if count < 1 {
				count = 1
			}
			priceMilli := int64(0)
			if msg.Gift != nil && msg.Gift.DiamondCount > 0 {
				// 抖音 diamondCount / 10 为元，统一转换为千分之一元。
				priceMilli = int64(msg.Gift.DiamondCount) * 100
			}
			event := Event{Type: EventGift, ReceivedAt: recvAt, EventTimestamp: timestamp, Name: giftName, Count: count, PriceMilli: priceMilli, UID: uid, Username: username}
			extra := map[string]interface{}{
				"gift_name": giftName, "num": count, "price_milli": priceMilli, "timestamp": recvAt.Unix(), "uid": uid,
			}
			r.addDouyinGift(event, extra, msg.ComboCount, msg.RepeatEnd, msg.GiftId)
		})
	}

	if err := r.client.Start(ctx); err != nil {
		r.discardWritersLocked()
		return err
	}

	r.running = true
	r.logger.Info("抖音弹幕录制已启动")

	return nil
}

// Stop 停止弹幕录制
func (r *DouyinDanmakuRecorder) Stop() {
	c := r.client
	if c != nil {
		c.Stop()
	}
	// 停止连接后先刷出尚未收到 RepeatEnd 的连击礼物，再关闭文件。
	r.flushDouyinCombos()
	w := r.stopBase()
	r.client = nil
	r.closeWriters(w)
	r.logger.Infof("抖音弹幕录制已停止，共录制 %d 条弹幕，输出文件: %v", r.GetCount(), r.OutputFiles())
}

// addDouyinGift 将抖音连击礼物按用户和礼物 ID 延迟合并为一条累计事件。
func (r *DouyinDanmakuRecorder) addDouyinGift(event Event, extra map[string]interface{}, comboCount, repeatEnd int32, giftID int64) {
	if comboCount <= 0 {
		r.addEvent(event, "gift", extra)
		return
	}
	key := fmt.Sprintf("%s:%d", event.UID, giftID)
	if event.UID == "" {
		key = fmt.Sprintf("%s:%s", event.Username, event.Name)
	}

	r.comboMu.Lock()
	if r.pendingCombo == nil {
		r.pendingCombo = make(map[string]*pendingDouyinGift)
	}
	pending, exists := r.pendingCombo[key]
	if exists {
		if event.Count > pending.event.Count {
			pending.event.Count = event.Count
		}
		if event.EventTimestamp > 0 {
			pending.event.EventTimestamp = event.EventTimestamp
		}
		pending.extra["num"] = pending.event.Count
	} else {
		pending = &pendingDouyinGift{event: event, extra: extra}
		r.pendingCombo[key] = pending
	}
	if pending.timer != nil {
		pending.timer.Stop()
	}
	if repeatEnd != 0 {
		delete(r.pendingCombo, key)
		r.comboMu.Unlock()
		r.addEvent(pending.event, "gift", pending.extra)
		return
	}
	pending.timer = time.AfterFunc(douyinComboWindow, func() {
		r.flushDouyinCombo(key)
	})
	r.comboMu.Unlock()
}

func (r *DouyinDanmakuRecorder) flushDouyinCombo(key string) {
	r.comboMu.Lock()
	pending, exists := r.pendingCombo[key]
	if exists {
		delete(r.pendingCombo, key)
	}
	r.comboMu.Unlock()
	if exists {
		r.addEvent(pending.event, "gift", pending.extra)
	}
}

func (r *DouyinDanmakuRecorder) flushDouyinCombos() {
	r.comboMu.Lock()
	pending := make([]*pendingDouyinGift, 0, len(r.pendingCombo))
	for key, gift := range r.pendingCombo {
		if gift.timer != nil {
			gift.timer.Stop()
		}
		pending = append(pending, gift)
		delete(r.pendingCombo, key)
	}
	r.comboMu.Unlock()
	for _, gift := range pending {
		r.addEvent(gift.event, "gift", gift.extra)
	}
}
