package danmaku

import (
	"strings"
	"time"
)

// EventType 表示统一弹幕事件类型。
type EventType string

const (
	// EventComment 普通弹幕。
	EventComment EventType = "comment"
	// EventGift 礼物事件。
	EventGift EventType = "gift"
	// EventSuperChat 醒目留言事件。
	EventSuperChat EventType = "super_chat"
	// EventGuard 上舰事件。
	EventGuard EventType = "guard"
)

// Event 是平台消息归一化后的内部事件。
type Event struct {
	Type           EventType
	ReceivedAt     time.Time
	EventTimestamp int64
	Text           string
	Color          int
	Mode           int
	UID            string
	Username       string
	Name           string
	Count          int64
	PriceMilli     int64
	Level          int
	Duration       int
	CoinType       string
}

// NormalizeEvent 补齐事件默认值并清理不能写入单行 XML 的文本。
func NormalizeEvent(event Event) Event {
	if event.ReceivedAt.IsZero() {
		event.ReceivedAt = time.Now()
	}
	if event.EventTimestamp <= 0 {
		event.EventTimestamp = event.ReceivedAt.UnixMilli()
	} else {
		event.EventTimestamp = normalizeTimestamp(event.EventTimestamp)
	}
	if event.Color <= 0 {
		event.Color = 16777215
	}
	if event.Mode <= 0 {
		event.Mode = 1
	}
	if event.Count < 1 && (event.Type == EventGift || event.Type == EventGuard) {
		event.Count = 1
	}
	event.Text = strings.NewReplacer("\r", "", "\n", "").Replace(event.Text)
	if event.PriceMilli < 0 {
		event.PriceMilli = 0
	}
	return event
}

// normalizeTimestamp 将秒、毫秒和纳秒时间戳统一为毫秒。
func normalizeTimestamp(timestamp int64) int64 {
	switch {
	// 纳秒时间戳通常为 1e18 量级；先转换为毫秒。
	case timestamp >= 100_000_000_000_000_000:
		return timestamp / 1_000_000
	// 1e11~1e14 为 Unix 毫秒（当前及可预见的时间范围），保持不变。
	case timestamp >= 100_000_000_000:
		return timestamp
	// 更小的正数按 Unix 秒处理。
	case timestamp > 0:
		return timestamp * 1_000
	default:
		return timestamp
	}
}
