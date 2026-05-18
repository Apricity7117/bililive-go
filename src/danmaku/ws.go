package danmaku

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const (
	wsGUID          = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"
	wsOpcodeText    = 1
	wsOpcodeBinary  = 2
	wsOpcodeClose   = 8
	wsOpcodePing    = 9
	wsOpcodePong    = 10
	wsReadTimeout   = 90 * time.Second
	wsWriteTimeout  = 10 * time.Second
	wsMaxPacketSize = 16 * 1024 * 1024
)

type wsConn struct {
	conn net.Conn
	r    *bufio.Reader
	mu   sync.Mutex
}

func dialWS(ctx context.Context, rawURL string, headers http.Header) (*wsConn, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "ws" && parsed.Scheme != "wss" {
		return nil, fmt.Errorf("不支持的 WebSocket 协议: %s", parsed.Scheme)
	}
	host := parsed.Host
	if !strings.Contains(host, ":") {
		if parsed.Scheme == "wss" {
			host += ":443"
		} else {
			host += ":80"
		}
	}

	var conn net.Conn
	if parsed.Scheme == "wss" {
		dialer := tls.Dialer{
			NetDialer: &net.Dialer{Timeout: 15 * time.Second},
			Config:    &tls.Config{ServerName: parsed.Hostname()},
		}
		conn, err = dialer.DialContext(ctx, "tcp", host)
	} else {
		dialer := net.Dialer{Timeout: 15 * time.Second}
		conn, err = dialer.DialContext(ctx, "tcp", host)
	}
	if err != nil {
		return nil, err
	}

	key, err := wsKey()
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	req.Host = parsed.Host
	req.Header.Set("Host", parsed.Host)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", key)
	req.Header.Set("Sec-WebSocket-Version", "13")
	for name, values := range headers {
		for _, value := range values {
			req.Header.Add(name, value)
		}
	}

	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket 握手失败: HTTP %d", resp.StatusCode)
	}
	if !validWSAccept(key, resp.Header.Get("Sec-WebSocket-Accept")) {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket 握手校验失败")
	}
	return &wsConn{conn: conn, r: reader}, nil
}

func (c *wsConn) ReadMessage() ([]byte, error) {
	var fragments []byte
	var messageOpcode byte
	for {
		opcode, fin, payload, err := c.readFrame()
		if err != nil {
			return nil, err
		}
		switch opcode {
		case wsOpcodeText, wsOpcodeBinary:
			messageOpcode = opcode
			fragments = append(fragments[:0], payload...)
			if fin {
				return fragments, nil
			}
		case 0:
			if messageOpcode == 0 {
				return nil, fmt.Errorf("收到无起始帧的 WebSocket 分片")
			}
			fragments = append(fragments, payload...)
			if len(fragments) > wsMaxPacketSize {
				return nil, fmt.Errorf("websocket 消息过大")
			}
			if fin {
				return fragments, nil
			}
		case wsOpcodePing:
			_ = c.writeFrame(wsOpcodePong, payload)
		case wsOpcodePong:
			continue
		case wsOpcodeClose:
			return nil, io.EOF
		default:
			continue
		}
	}
}

func (c *wsConn) WriteBinary(payload []byte) error {
	return c.writeFrame(wsOpcodeBinary, payload)
}

func (c *wsConn) Close() error {
	_ = c.writeFrame(wsOpcodeClose, nil)
	return c.conn.Close()
}

func (c *wsConn) readFrame() (opcode byte, fin bool, payload []byte, err error) {
	_ = c.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	header := make([]byte, 2)
	if _, err = io.ReadFull(c.r, header); err != nil {
		return 0, false, nil, err
	}
	fin = header[0]&0x80 != 0
	opcode = header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := uint64(header[1] & 0x7f)
	switch length {
	case 126:
		extended := make([]byte, 2)
		if _, err = io.ReadFull(c.r, extended); err != nil {
			return 0, false, nil, err
		}
		length = uint64(binary.BigEndian.Uint16(extended))
	case 127:
		extended := make([]byte, 8)
		if _, err = io.ReadFull(c.r, extended); err != nil {
			return 0, false, nil, err
		}
		length = binary.BigEndian.Uint64(extended)
	}
	if length > wsMaxPacketSize {
		return 0, false, nil, fmt.Errorf("websocket 帧过大: %d", length)
	}
	var maskKey [4]byte
	if masked {
		if _, err = io.ReadFull(c.r, maskKey[:]); err != nil {
			return 0, false, nil, err
		}
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(c.r, payload); err != nil {
		return 0, false, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return opcode, fin, payload, nil
}

func (c *wsConn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	var maskKey [4]byte
	if _, err := rand.Read(maskKey[:]); err != nil {
		return err
	}
	header := []byte{0x80 | opcode}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, byte(0x80|length))
	case length <= 0xffff:
		header = append(header, 0x80|126, 0, 0)
		binary.BigEndian.PutUint16(header[len(header)-2:], uint16(length))
	default:
		header = append(header, 0x80|127, 0, 0, 0, 0, 0, 0, 0, 0)
		binary.BigEndian.PutUint64(header[len(header)-8:], uint64(length))
	}
	header = append(header, maskKey[:]...)
	masked := make([]byte, len(payload))
	for i := range payload {
		masked[i] = payload[i] ^ maskKey[i%4]
	}
	if _, err := c.conn.Write(header); err != nil {
		return err
	}
	_, err := c.conn.Write(masked)
	return err
}

func wsKey() (string, error) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(key[:]), nil
}

func validWSAccept(key, accept string) bool {
	sum := sha1.Sum([]byte(key + wsGUID))
	expected := base64.StdEncoding.EncodeToString(sum[:])
	return strings.TrimSpace(accept) == expected
}
