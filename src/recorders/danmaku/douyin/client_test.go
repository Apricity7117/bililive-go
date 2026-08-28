package douyin

import (
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/sirupsen/logrus"
)

// TestPhase1Connection 测试不带签名的连接（需要手动提供 roomID 参数）
func TestPhase1Connection(t *testing.T) {
	t.Skip("需要通过环境变量测试，请使用 TestWithCookies")
}

// TestWithCookies 带完整 cookies 测试
// DOUYIN_ROOM=400604888272 DOUYIN_COOKIES="ttwid=xxx; ..." go test -v -timeout 60s -run TestWithCookies
func TestWithCookies(t *testing.T) {
	roomID := os.Getenv("DOUYIN_ROOM")
	cookies := os.Getenv("DOUYIN_COOKIES")
	if roomID == "" || cookies == "" {
		t.Skip("请设置环境变量: DOUYIN_ROOM=<roomID> DOUYIN_COOKIES=<cookies>")
	}
	testConnection(t, roomID, cookies)
}

func testConnection(t *testing.T, roomID, cookies string) {
	logger := logrus.New().WithField("test", "douyin")

	// Step 1: 获取 ttwid
	t.Log("Step 1: 获取 ttwid...")
	ttwid := getTtwidFromCookies(cookies)
	if ttwid == "" {
		var err error
		ttwid, err = fetchTtwid(logger)
		if err != nil {
			t.Fatalf("获取 ttwid 失败: %v", err)
		}
	}
	t.Logf("ttwid: %s...", ttwid[:min(20, len(ttwid))])

	// Step 2: 获取真实 roomId
	t.Log("Step 2: 获取真实 roomId...")
	realRoomID, err := fetchRealRoomID(roomID, cookies, logger)
	if err != nil {
		t.Logf("获取真实 roomId 失败，使用原始值: %v", err)
		realRoomID = roomID
	}
	t.Logf("realRoomID: %s", realRoomID)

	// Step 3: 生成 user_unique_id
	userUniqueID := generateUserUniqueID()
	t.Logf("userUniqueID: %s", userUniqueID)

	// Step 4: 构建 URL
	wsURL := buildWSURL(realRoomID, ttwid, userUniqueID)

	// Step 5: 生成 signature
	t.Log("Step 5: 生成 signature...")
	signature, err := generateSignature(wsURL, logger)
	if err != nil {
		t.Fatalf("生成 signature 失败: %v", err)
	}
	t.Logf("signature: %s...", signature[:min(30, len(signature))])
	wsURL += "&signature=" + signature

	t.Logf("WebSocket URL (前120字符): %s...", wsURL[:min(120, len(wsURL))])

	// Step 6: 尝试连接
	t.Log("Step 6: 尝试 WebSocket 连接...")

	header := http.Header{}
	header.Set("User-Agent", userAgent)
	header.Set("Origin", "https://live.douyin.com")
	if cookies != "" {
		header.Set("Cookie", cookies)
	} else {
		header.Set("Cookie", "ttwid="+ttwid)
	}

	dialer := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, dialErr := dialer.Dial(wsURL, header)

	if resp != nil {
		t.Logf("WebSocket 响应状态码: %d", resp.StatusCode)
		t.Logf("Handshake-Msg: %s", resp.Header.Get("Handshake-Msg"))
		t.Logf("Handshake-Status: %s", resp.Header.Get("Handshake-Status"))
		for k, v := range resp.Header {
			if k != "Server" && k != "Date" && k != "Via" && k != "Server-Timing" &&
				k != "X-Dsa-Origin-Status" && k != "X-Dsa-Trace-Id" && k != "X-Request-Ip" &&
				k != "X-Tt-Logid" && k != "X-Tt-Trace-Host" && k != "X-Tt-Trace-Id" && k != "X-Tt-Trace-Tag" {
				t.Logf("  %s: %v", k, v)
			}
		}
		resp.Body.Close()
	}

	if dialErr != nil {
		t.Fatalf("WebSocket 连接失败: %v", dialErr)
	}
	defer conn.Close()

	t.Log("连接成功！等待消息...")
	msgCount := 0
	timer := time.After(15 * time.Second)

	for {
		select {
		case <-timer:
			t.Logf("测试完成！共收到 %d 条消息", msgCount)
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		_, message, readErr := conn.ReadMessage()
		if readErr != nil {
			t.Logf("读取消息结束 (已收到 %d 条): %v", msgCount, readErr)
			return
		}

		msgCount++
		frame := &PushFrame{}
		if parseErr := frame.Unmarshal(message); parseErr != nil {
			t.Logf("消息 #%d: 解析 PushFrame 失败: %v", msgCount, parseErr)
			continue
		}

		t.Logf("消息 #%d: seqId=%d, logId=%d, service=%d, payloadType=%q, payloadLen=%d",
			msgCount, frame.SeqId, frame.LogId, frame.Service, frame.PayloadType, len(frame.Payload))

		if len(frame.Payload) > 0 {
			// 打印前 50 字节的 hex
			hexDump := fmt.Sprintf("%x", frame.Payload[:min(50, len(frame.Payload))])
			t.Logf("  payload hex: %s", hexDump)

			decompressed, gzipErr := gzipDecompress(frame.Payload)
			if gzipErr != nil {
				t.Logf("  GZIP 解压失败: %v (跳过)", gzipErr)
				continue
			}
			resp := &Response{}
			if respErr := resp.Unmarshal(decompressed); respErr != nil {
				t.Logf("  解析 Response 失败: %v", respErr)
				continue
			}
			t.Logf("  Response: %d 条消息, needAck=%v, heartbeatDuration=%d",
				len(resp.MessagesList), resp.NeedAck, resp.HeartbeatDuration)
			for i, msg := range resp.MessagesList {
				t.Logf("    消息[%d]: method=%s, payloadLen=%d", i, msg.Method, len(msg.Payload))
				// 对 ChatMessage 打印原始 hex 帮助调试
				if msg.Method == "WebcastChatMessage" && len(msg.Payload) > 0 {
					hexDump := fmt.Sprintf("%x", msg.Payload)
					t.Logf("      ChatMessage hex (%d bytes): %s", len(msg.Payload), hexDump[:min(400, len(hexDump))])
				}
			}
		}
	}
}
