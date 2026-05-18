package danmaku

import "time"

type MessageType string

const (
	MessageTypeComment   MessageType = "comment"
	MessageTypeGift      MessageType = "give_gift"
	MessageTypeSuperChat MessageType = "super_chat"
	MessageTypeGuard     MessageType = "guard"
)

// Sender 是弹幕事件的发送者信息。
type Sender struct {
	UID    string `json:"uid,omitempty"`
	Name   string `json:"name,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

// Message 是写入弹幕文件的统一事件结构。
type Message struct {
	Type      MessageType `json:"type"`
	Timestamp int64       `json:"timestamp"`
	Text      string      `json:"text,omitempty"`
	Color     string      `json:"color,omitempty"`
	Mode      int         `json:"mode,omitempty"`
	Sender    Sender      `json:"sender,omitempty"`

	Name     string  `json:"name,omitempty"`
	Count    int64   `json:"count,omitempty"`
	Price    float64 `json:"price,omitempty"`
	Level    int     `json:"level,omitempty"`
	Duration int     `json:"duration,omitempty"`
}

// Metadata 描述一次录制对应的直播间和时间信息。
type Metadata struct {
	Platform             string    `json:"platform,omitempty"`
	RoomID               string    `json:"room_id,omitempty"`
	RoomTitle            string    `json:"room_title,omitempty"`
	UserName             string    `json:"user_name,omitempty"`
	RecordStartTimestamp int64     `json:"record_start_timestamp"`
	LiveStartTimestamp   int64     `json:"live_start_timestamp,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}
