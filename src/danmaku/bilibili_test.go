package danmaku

import (
	"testing"
	"time"
)

func TestParseBilibiliComment(t *testing.T) {
	payload := map[string]any{
		"cmd": "DANMU_MSG",
		"info": []any{
			[]any{0, 1, 25, 16711680, 1700000000000, 0, "", "", 0},
			" 测试弹幕\n",
			[]any{float64(42), "测试用户"},
		},
	}
	message, ok := parseBilibiliComment(payload, true)
	if !ok {
		t.Fatal("parseBilibiliComment returned false")
	}
	if message.Timestamp != 1700000000000 {
		t.Fatalf("Timestamp = %d", message.Timestamp)
	}
	if message.Text != "测试弹幕" {
		t.Fatalf("Text = %q", message.Text)
	}
	if message.Color != "#ff0000" {
		t.Fatalf("Color = %q", message.Color)
	}
	if message.Sender.UID != "42" || message.Sender.Name != "测试用户" {
		t.Fatalf("Sender = %+v", message.Sender)
	}
}

func TestParseBilibiliCommentUsesLocalTimestamp(t *testing.T) {
	payload := map[string]any{
		"cmd": "DANMU_MSG",
		"info": []any{
			[]any{0, 1, 25, 16711680, 1700000000000, 0, "", "", 0},
			"测试弹幕",
			[]any{float64(42), "测试用户"},
		},
	}
	start := time.Now().UnixMilli()
	message, ok := parseBilibiliComment(payload, false)
	end := time.Now().UnixMilli()
	if !ok {
		t.Fatal("parseBilibiliComment returned false")
	}
	if message.Timestamp < start || message.Timestamp > end {
		t.Fatalf("Timestamp = %d, want between %d and %d", message.Timestamp, start, end)
	}
}

func TestParseBilibiliGift(t *testing.T) {
	payload := map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"timestamp": float64(1700000000),
			"giftName":  "辣条",
			"num":       float64(3),
			"price":     float64(1000),
			"uid":       float64(42),
			"uname":     "测试用户",
		},
	}
	message, ok := parseBilibiliGift(payload, true)
	if !ok {
		t.Fatal("parseBilibiliGift returned false")
	}
	if message.Name != "辣条" || message.Count != 3 || message.Price != 1 {
		t.Fatalf("message = %+v", message)
	}
	if message.Timestamp != 1700000000000 {
		t.Fatalf("Timestamp = %d", message.Timestamp)
	}
}
