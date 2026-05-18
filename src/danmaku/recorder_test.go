package danmaku

import (
	"testing"

	"github.com/bililive-go/bililive-go/src/configs"
)

type fakeStreamWriter struct {
	messages []Message
}

func (w *fakeStreamWriter) AddMessage(message Message) error {
	w.messages = append(w.messages, message)
	return nil
}

func (w *fakeStreamWriter) Close() error {
	return nil
}

func (w *fakeStreamWriter) Path() string {
	return "fake.xml"
}

func TestRecorderWriteMessageFiltersGiftWhenDisabled(t *testing.T) {
	writer := &fakeStreamWriter{}
	recorder := &Recorder{
		cfg:     configs.DanmakuRecord{SaveGift: false},
		writers: []streamWriter{writer},
	}

	recorder.writeMessage(Message{Type: MessageTypeComment, Text: "弹幕"})
	recorder.writeMessage(Message{Type: MessageTypeGift, Name: "礼物"})
	recorder.writeMessage(Message{Type: MessageTypeGuard, Name: "舰长"})
	recorder.writeMessage(Message{Type: MessageTypeSuperChat, Text: "SC"})

	if len(writer.messages) != 2 {
		t.Fatalf("messages = %+v", writer.messages)
	}
	if writer.messages[0].Type != MessageTypeComment || writer.messages[1].Type != MessageTypeSuperChat {
		t.Fatalf("messages = %+v", writer.messages)
	}
}

func TestRecorderWriteMessageKeepsGiftWhenEnabled(t *testing.T) {
	writer := &fakeStreamWriter{}
	recorder := &Recorder{
		cfg:     configs.DanmakuRecord{SaveGift: true},
		writers: []streamWriter{writer},
	}

	recorder.writeMessage(Message{Type: MessageTypeGift, Name: "礼物"})
	recorder.writeMessage(Message{Type: MessageTypeGuard, Name: "舰长"})

	if len(writer.messages) != 2 {
		t.Fatalf("messages = %+v", writer.messages)
	}
}
