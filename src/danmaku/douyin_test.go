package danmaku

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestFindDouyinBundle(t *testing.T) {
	dir := t.TempDir()
	bundlePath := filepath.Join(dir, "index-test.cjs")
	if err := os.WriteFile(bundlePath, []byte("class DouYinDanmaClient {}\nconst method = 'WebcastChatMessage';"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := findDouyinBundle(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got != bundlePath {
		t.Fatalf("bundle = %q, want %q", got, bundlePath)
	}
}

func TestDouyinScanMessages(t *testing.T) {
	client := &douyinClient{}
	var messages []Message
	input := bytes.NewBufferString(`{"type":"comment","timestamp":1700000000000,"text":"测试弹幕","sender":{"uid":"42","name":"用户"}}` + "\n")

	if err := client.scanMessages(input, func(message Message) {
		messages = append(messages, message)
	}); err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %+v", messages)
	}
	if messages[0].Type != MessageTypeComment || messages[0].Text != "测试弹幕" {
		t.Fatalf("message = %+v", messages[0])
	}
}
