package danmaku

import "github.com/bililive-go/bililive-go/src/configs"

// Writer 是 ASS/XML 输出器共享的最小接口。
type Writer interface {
	Format() configs.DanmakuFormat
	Path() string
	WriteEvent(Event) error
	Close() error
}

// eventWriter 保留内部命名，避免影响已有实现。
type eventWriter = Writer

// assEventWriter 将统一事件适配到现有 ASS 排布实现。
type assEventWriter struct {
	writer *AssWriter
}

func (w *assEventWriter) Format() configs.DanmakuFormat { return configs.DanmakuFormatASS }

func (w *assEventWriter) Path() string { return w.writer.OutputPath() }

func (w *assEventWriter) WriteEvent(event Event) error {
	event = NormalizeEvent(event)
	switch event.Type {
	case EventComment:
		w.writer.AddDanmaku(event.ReceivedAt, event.Username, event.Text, event.Color)
	case EventGift:
		// ASS 的礼物接口沿用 B 站金瓜子单位（1 元 = 1000），而 Event
		// 已将其统一为千分之一元整数；gold 类型可直接传回原整数。
		price := 0
		if event.CoinType == "gold" {
			price = int(event.PriceMilli)
		}
		w.writer.AddGift(event.ReceivedAt, event.Username, event.Name, int(event.Count), price, event.CoinType)
	case EventSuperChat:
		w.writer.AddSuperChat(event.ReceivedAt, event.Username, event.Text, int(event.PriceMilli/1000))
	case EventGuard:
		// 上舰价格同样来自 B 站金瓜子字段，ASS 需要原始金瓜子整数。
		w.writer.AddGuard(event.ReceivedAt, event.Username, event.Name, int(event.PriceMilli))
	}
	return w.writer.WriteError()
}

func (w *assEventWriter) Close() error { return w.writer.Close() }
