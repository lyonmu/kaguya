package chat

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 关闭全部 WS 连接必须能打断服务端的阻塞读循环，且关停后拒绝新连接。
// HTTP Server.Shutdown 不会关闭 hijack 的连接，因此这是应用侧必须补的语义。
func TestCloseAllChatWSStopsConnectionsAndRejectsNew(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.GET("/v1/chat/ws", (&ChatApiV1Group{}).ChatWS)
	server := httptest.NewServer(engine)
	defer server.Close()

	dial := func() *websocket.Conn {
		conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/chat/ws", nil)
		if err != nil {
			t.Fatalf("dial websocket: %v", err)
		}
		return conn
	}

	first := dial()
	releaseChatWSForTestReset(t)
	// 等待 handler 完成登记，避免在注册前关闭。
	deadline := time.Now().Add(2 * time.Second)
	for activeChatWSCount() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if activeChatWSCount() == 0 {
		t.Fatal("websocket handler did not register the connection")
	}

	CloseAllChatWS()
	_ = first.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("connection must be closed during shutdown")
	}
	_ = first.Close()

	// 关停后新连接立刻被拒绝，不进入读循环。
	second, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(server.URL, "http")+"/v1/chat/ws", nil)
	if err == nil {
		_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
		if _, _, readErr := second.ReadMessage(); readErr == nil {
			t.Fatal("new connection must be rejected after shutdown")
		}
		_ = second.Close()
	}

	// 等待被关闭连接的 handler 完成注销。
	deadline = time.Now().Add(2 * time.Second)
	for activeChatWSCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := activeChatWSCount(); n != 0 {
		t.Fatalf("closed connections not unregistered: %d", n)
	}
}

// activeChatWSCount 返回已登记的连接数，仅供测试观测。
func activeChatWSCount() int {
	chatWSRegistry.Lock()
	defer chatWSRegistry.Unlock()
	return len(chatWSRegistry.conns)
}

// releaseChatWSForTestReset 恢复注册表的 stopping 状态，保证测试间互不影响。
func releaseChatWSForTestReset(t *testing.T) {
	t.Helper()
	chatWSRegistry.Lock()
	chatWSRegistry.stopping = false
	chatWSRegistry.Unlock()
	t.Cleanup(func() {
		chatWSRegistry.Lock()
		chatWSRegistry.stopping = false
		chatWSRegistry.Unlock()
	})
}
