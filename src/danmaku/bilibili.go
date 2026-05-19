package danmaku

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bililive-go/bililive-go/src/live"
	"github.com/bililive-go/bililive-go/src/pkg/livelogger"
	"github.com/bililive-go/bililive-go/src/pkg/utils"
)

const (
	bilibiliRoomInitURL   = "https://api.live.bilibili.com/room/v1/Room/room_init"
	bilibiliDanmuInfoURL  = "https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo"
	bilibiliDefaultWSURL  = "wss://broadcastlv.chat.bilibili.com/sub"
	bilibiliUserAgent     = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
	bilibiliHeaderLength  = 16
	bilibiliProtoPlain    = 1
	bilibiliProtoZlib     = 2
	bilibiliOpHeartbeat   = 2
	bilibiliOpMessage     = 5
	bilibiliOpAuth        = 7
	bilibiliOpAuthReply   = 8
	bilibiliHeartbeatTick = 30 * time.Second
)

type bilibiliClient struct {
	liveObj            live.Live
	roomID             string
	logger             *livelogger.LiveLogger
	useServerTimestamp bool
	useCookie          bool

	mu   sync.Mutex
	conn *wsConn
}

func newBilibiliClient(liveObj live.Live, roomID string, logger *livelogger.LiveLogger, useServerTimestamp bool, useCookie bool) *bilibiliClient {
	return &bilibiliClient{
		liveObj:            liveObj,
		roomID:             roomID,
		logger:             logger,
		useServerTimestamp: useServerTimestamp,
		useCookie:          useCookie,
	}
}

func (c *bilibiliClient) Listen(ctx context.Context, onMessage func(Message)) error {
	for {
		if ctx.Err() != nil {
			return nil
		}
		if err := c.listenOnce(ctx, onMessage); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			c.logger.WithError(err).Warn("B站弹幕连接异常，2 秒后重试")
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
			}
			continue
		}
		return nil
	}
}

func (c *bilibiliClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *bilibiliClient) listenOnce(ctx context.Context, onMessage func(Message)) error {
	roomID, err := c.resolveRoomID(ctx)
	if err != nil {
		return err
	}
	if roomID != "" {
		c.roomID = roomID
	}

	token, wsURL, err := c.getDanmuInfo(ctx, c.roomID)
	if err != nil {
		c.logger.WithError(err).Warn("获取 B站弹幕服务器失败，回退到默认弹幕服务器")
		wsURL = bilibiliDefaultWSURL
	}

	headers := http.Header{}
	headers.Set("User-Agent", bilibiliUserAgent)
	if cookie := c.cookieHeader(); cookie != "" {
		headers.Set("Cookie", cookie)
	}
	conn, err := dialWS(ctx, wsURL, headers)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.mu.Unlock()
	defer c.Close()

	if err := c.sendAuth(conn, token); err != nil {
		return err
	}
	heartbeatStop := make(chan struct{})
	go c.heartbeatLoop(conn, heartbeatStop)
	defer close(heartbeatStop)

	for {
		payload, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		if err := c.handlePacket(payload, onMessage); err != nil {
			c.logger.WithError(err).Debug("解析 B站弹幕包失败")
		}
	}
}

func (c *bilibiliClient) heartbeatLoop(conn *wsConn, stop <-chan struct{}) {
	ticker := time.NewTicker(bilibiliHeartbeatTick)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			_ = conn.WriteBinary(packBilibiliPacket(bilibiliOpHeartbeat, bilibiliProtoPlain, []byte("[object Object]")))
		case <-stop:
			return
		}
	}
}

func (c *bilibiliClient) sendAuth(conn *wsConn, token string) error {
	uid := c.uidFromCookie()
	roomID, _ := strconv.ParseInt(c.roomID, 10, 64)
	payload := map[string]any{
		"uid":      uid,
		"roomid":   roomID,
		"protover": bilibiliProtoZlib,
		"platform": "web",
		"type":     2,
		"key":      token,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return conn.WriteBinary(packBilibiliPacket(bilibiliOpAuth, bilibiliProtoPlain, data))
}

func (c *bilibiliClient) handlePacket(data []byte, onMessage func(Message)) error {
	for len(data) >= bilibiliHeaderLength {
		packetLen := int(binary.BigEndian.Uint32(data[0:4]))
		if packetLen < bilibiliHeaderLength || packetLen > len(data) {
			return fmt.Errorf("无效弹幕包长度: %d", packetLen)
		}
		headerLen := int(binary.BigEndian.Uint16(data[4:6]))
		version := int(binary.BigEndian.Uint16(data[6:8]))
		op := int(binary.BigEndian.Uint32(data[8:12]))
		if headerLen > packetLen {
			return fmt.Errorf("无效弹幕包头长度: %d", headerLen)
		}
		body := data[headerLen:packetLen]
		switch op {
		case bilibiliOpMessage:
			switch version {
			case bilibiliProtoZlib:
				unpacked, err := unzip(body)
				if err != nil {
					return err
				}
				if err := c.handlePacket(unpacked, onMessage); err != nil {
					return err
				}
			default:
				c.handleCommand(body, onMessage)
			}
		case bilibiliOpAuthReply:
			c.logger.Debug("B站弹幕认证成功")
		}
		data = data[packetLen:]
	}
	return nil
}

func (c *bilibiliClient) handleCommand(body []byte, onMessage func(Message)) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return
	}
	cmd := stringValue(payload["cmd"])
	if idx := strings.Index(cmd, ":"); idx >= 0 {
		cmd = cmd[:idx]
	}
	var (
		message Message
		ok      bool
	)
	switch cmd {
	case "DANMU_MSG":
		message, ok = parseBilibiliComment(payload, c.useServerTimestamp)
	case "SEND_GIFT":
		message, ok = parseBilibiliGift(payload, c.useServerTimestamp)
	case "SUPER_CHAT_MESSAGE":
		message, ok = parseBilibiliSuperChat(payload, c.useServerTimestamp)
	case "GUARD_BUY":
		message, ok = parseBilibiliGuard(payload, c.useServerTimestamp)
	}
	if ok {
		onMessage(message)
	}
}

func (c *bilibiliClient) resolveRoomID(ctx context.Context) (string, error) {
	parsed, err := url.Parse(c.liveObj.GetRawUrl())
	if err != nil {
		return "", err
	}
	roomID := c.roomID
	if roomID == "" || roomID == "." || roomID == "/" {
		roomID = strings.Trim(strings.Split(strings.Trim(parsed.Path, "/"), "/")[0], " ")
	}
	if roomID == "" {
		return "", fmt.Errorf("b站房间号为空")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bilibiliRoomInitURL+"?id="+url.QueryEscape(roomID), nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", bilibiliUserAgent)
	if cookie := c.cookieHeader(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := utils.CreateDefaultClient().Do(req)
	if err != nil {
		return roomID, nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return roomID, nil
	}
	var decoded struct {
		Code int `json:"code"`
		Data struct {
			RoomID int64 `json:"room_id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return roomID, nil
	}
	if decoded.Code != 0 || decoded.Data.RoomID == 0 {
		return roomID, nil
	}
	return strconv.FormatInt(decoded.Data.RoomID, 10), nil
}

func (c *bilibiliClient) getDanmuInfo(ctx context.Context, roomID string) (token string, wsURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, bilibiliDanmuInfoURL+"?type=0&id="+url.QueryEscape(roomID), nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", bilibiliUserAgent)
	if cookie := c.cookieHeader(); cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	resp, err := utils.CreateDefaultClient().Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("b站 getDanmuInfo 返回 HTTP %d", resp.StatusCode)
	}
	var decoded struct {
		Code int `json:"code"`
		Data struct {
			Token    string `json:"token"`
			HostList []struct {
				Host    string `json:"host"`
				WssPort int    `json:"wss_port"`
				WsPort  int    `json:"ws_port"`
			} `json:"host_list"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return "", "", err
	}
	if decoded.Code != 0 {
		return "", "", fmt.Errorf("b站 getDanmuInfo 返回异常 code=%d", decoded.Code)
	}
	if len(decoded.Data.HostList) == 0 {
		return decoded.Data.Token, bilibiliDefaultWSURL, nil
	}
	host := decoded.Data.HostList[0]
	if host.WssPort > 0 {
		return decoded.Data.Token, fmt.Sprintf("wss://%s:%d/sub", host.Host, host.WssPort), nil
	}
	if host.WsPort > 0 {
		return decoded.Data.Token, fmt.Sprintf("ws://%s:%d/sub", host.Host, host.WsPort), nil
	}
	return decoded.Data.Token, bilibiliDefaultWSURL, nil
}

func (c *bilibiliClient) cookieHeader() string {
	if !c.useCookie {
		return ""
	}
	options := c.liveObj.GetOptions()
	if options == nil || options.Cookies == nil {
		return ""
	}
	parsed, err := url.Parse(c.liveObj.GetRawUrl())
	if err != nil {
		return ""
	}
	cookies := options.Cookies.Cookies(parsed)
	parts := make([]string, 0, len(cookies))
	for _, cookie := range cookies {
		parts = append(parts, cookie.Name+"="+cookie.Value)
	}
	return strings.Join(parts, "; ")
}

func (c *bilibiliClient) uidFromCookie() int64 {
	for _, part := range strings.Split(c.cookieHeader(), ";") {
		part = strings.TrimSpace(part)
		if strings.HasPrefix(part, "DedeUserID=") {
			uid, _ := strconv.ParseInt(strings.TrimPrefix(part, "DedeUserID="), 10, 64)
			return uid
		}
	}
	return 0
}

func packBilibiliPacket(op, version int, payload []byte) []byte {
	packetLen := bilibiliHeaderLength + len(payload)
	data := make([]byte, packetLen)
	binary.BigEndian.PutUint32(data[0:4], uint32(packetLen))
	binary.BigEndian.PutUint16(data[4:6], bilibiliHeaderLength)
	binary.BigEndian.PutUint16(data[6:8], uint16(version))
	binary.BigEndian.PutUint32(data[8:12], uint32(op))
	binary.BigEndian.PutUint32(data[12:16], 1)
	copy(data[bilibiliHeaderLength:], payload)
	return data
}

func unzip(data []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}

func parseBilibiliComment(payload map[string]any, useServerTimestamp bool) (Message, bool) {
	info := arrayValue(payload["info"])
	if len(info) < 3 {
		return Message{}, false
	}
	meta := arrayValue(info[0])
	user := arrayValue(info[2])
	content := strings.TrimSpace(stringValue(info[1]))
	content = strings.NewReplacer("\r", "", "\n", "").Replace(content)
	if content == "" {
		return Message{}, false
	}
	uid := intString(arrayItem(user, 0))
	return Message{
		Type:      MessageTypeComment,
		Timestamp: bilibiliEventTimestamp(arrayItem(meta, 4), useServerTimestamp),
		Text:      content,
		Color:     colorHex(intValue(arrayItem(meta, 3), 16777215)),
		Mode:      int(intValue(arrayItem(meta, 1), 1)),
		Sender: Sender{
			UID:  uid,
			Name: stringValue(arrayItem(user, 1)),
		},
	}, true
}

func parseBilibiliGift(payload map[string]any, useServerTimestamp bool) (Message, bool) {
	data := mapValue(payload["data"])
	if len(data) == 0 {
		return Message{}, false
	}
	price := floatValue(data["price"]) / 1000
	if stringValue(data["coin_type"]) == "silver" {
		price = 0
	}
	return Message{
		Type:      MessageTypeGift,
		Timestamp: bilibiliEventTimestamp(data["timestamp"], useServerTimestamp),
		Name:      firstString(data, "giftName", "gift_name"),
		Count:     intValue(firstValue(data, "num", "gift_num"), 1),
		Price:     price,
		Sender: Sender{
			UID:    intString(data["uid"]),
			Name:   stringValue(data["uname"]),
			Avatar: stringValue(data["face"]),
		},
	}, true
}

func parseBilibiliSuperChat(payload map[string]any, useServerTimestamp bool) (Message, bool) {
	data := mapValue(payload["data"])
	if len(data) == 0 {
		return Message{}, false
	}
	userInfo := mapValue(data["user_info"])
	return Message{
		Type:      MessageTypeSuperChat,
		Timestamp: bilibiliEventTimestamp(data["send_time"], useServerTimestamp),
		Text:      strings.NewReplacer("\r", "", "\n", "").Replace(stringValue(data["message"])),
		Price:     floatValue(data["price"]),
		Duration:  int(intValue(data["time"], 0)),
		Sender: Sender{
			UID:    intString(data["uid"]),
			Name:   stringValue(userInfo["uname"]),
			Avatar: stringValue(userInfo["face"]),
		},
	}, true
}

func parseBilibiliGuard(payload map[string]any, useServerTimestamp bool) (Message, bool) {
	data := mapValue(payload["data"])
	if len(data) == 0 {
		return Message{}, false
	}
	serverTimestamp := firstValue(data, "timestamp", "start_time")
	if serverTimestamp == nil {
		serverTimestamp = firstValue(payload, "timestamp")
	}
	return Message{
		Type:      MessageTypeGuard,
		Timestamp: bilibiliEventTimestamp(serverTimestamp, useServerTimestamp),
		Name:      stringValue(data["gift_name"]),
		Count:     1,
		Price:     floatValue(data["price"]) / 1000,
		Level:     int(intValue(data["guard_level"], 0)),
		Sender: Sender{
			UID:  intString(data["uid"]),
			Name: stringValue(data["username"]),
		},
	}, true
}

func bilibiliEventTimestamp(serverValue any, useServerTimestamp bool) int64 {
	now := time.Now().UnixMilli()
	if !useServerTimestamp {
		return now
	}
	timestamp := intValue(serverValue, 0)
	if timestamp <= 0 {
		return now
	}
	return normalizeTimestamp(timestamp)
}

func arrayValue(value any) []any {
	if arr, ok := value.([]any); ok {
		return arr
	}
	return nil
}

func arrayItem(arr []any, index int) any {
	if index < 0 || index >= len(arr) {
		return nil
	}
	return arr[index]
}

func mapValue(value any) map[string]any {
	if m, ok := value.(map[string]any); ok {
		return m
	}
	return nil
}

func stringValue(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case json.Number:
		return v.String()
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int64:
		return strconv.FormatInt(v, 10)
	case int:
		return strconv.Itoa(v)
	default:
		return ""
	}
}

func intValue(value any, fallback int64) int64 {
	switch v := value.(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		i, err := v.Int64()
		if err == nil {
			return i
		}
	case string:
		i, err := strconv.ParseInt(v, 10, 64)
		if err == nil {
			return i
		}
	}
	return fallback
}

func floatValue(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int64:
		return float64(v)
	case int:
		return float64(v)
	case json.Number:
		f, err := v.Float64()
		if err == nil {
			return f
		}
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err == nil {
			return f
		}
	}
	return 0
}

func intString(value any) string {
	return strconv.FormatInt(intValue(value, 0), 10)
}

func colorHex(color int64) string {
	return fmt.Sprintf("#%06x", color)
}

func firstValue(data map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := data[key]; ok {
			return value
		}
	}
	return nil
}

func firstString(data map[string]any, keys ...string) string {
	return stringValue(firstValue(data, keys...))
}
