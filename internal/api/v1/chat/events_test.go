package chat

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	dtochat "github.com/lyonmu/kaguya/internal/dto/chat"
	dtocode "github.com/lyonmu/kaguya/internal/dto/code"
	serviceagent "github.com/lyonmu/kaguya/internal/service/agent"
)

func TestChatEventsPublishesCommittedTitle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	publisher := serviceagent.NewTitlePublisher()
	previous := serviceagent.DefaultTitlePublisher
	serviceagent.DefaultTitlePublisher = publisher
	defer func() {
		serviceagent.DefaultTitlePublisher = previous
		publisher.Close()
	}()

	engine := gin.New()
	engine.GET("/v1/events", (&ChatApiV1Group{}).ChatEvents)
	server := httptest.NewServer(engine)
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("status=%d content-type=%s", response.StatusCode, response.Header.Get("Content-Type"))
	}

	// 响应头已返回说明订阅已注册；发布后应在流上收到完整 envelope。
	publisher.Publish(dtochat.TitleEvent{
		Type: dtochat.TitleEventUpdated, ConversationID: "conv-1", Revision: 3, Title: "新标题",
	})

	reader := bufio.NewReader(response.Body)
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	lines := make(chan string, 1)
	go func() {
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				close(lines)
				return
			}
			if strings.HasPrefix(line, "data: ") {
				lines <- strings.TrimSpace(strings.TrimPrefix(line, "data: "))
				return
			}
		}
	}()
	select {
	case raw, ok := <-lines:
		if !ok {
			t.Fatal("event stream closed before delivering the event")
		}
		var envelope dtocode.Response
		if err := json.Unmarshal([]byte(raw), &envelope); err != nil {
			t.Fatalf("decode event: %v (%s)", err, raw)
		}
		if envelope.Code != dtocode.SystemSuccess.Code {
			t.Fatalf("unexpected envelope: %s", raw)
		}
		data, err := json.Marshal(envelope.Data)
		if err != nil {
			t.Fatal(err)
		}
		var event dtochat.TitleEvent
		if err := json.Unmarshal(data, &event); err != nil {
			t.Fatal(err)
		}
		if event != (dtochat.TitleEvent{Type: dtochat.TitleEventUpdated, ConversationID: "conv-1", Revision: 3, Title: "新标题"}) {
			t.Fatalf("event mismatch: %+v", event)
		}
	case <-deadline.C:
		t.Fatal("timed out waiting for the title event")
	}
}

func TestChatEventsFailsWhenPublisherIsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	publisher := serviceagent.NewTitlePublisher()
	publisher.Close()
	previous := serviceagent.DefaultTitlePublisher
	serviceagent.DefaultTitlePublisher = publisher
	defer func() { serviceagent.DefaultTitlePublisher = previous }()

	engine := gin.New()
	engine.GET("/v1/events", (&ChatApiV1Group{}).ChatEvents)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	engine.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d", recorder.Code)
	}
	var envelope dtocode.Response
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v (%s)", err, recorder.Body)
	}
	if envelope.Code != dtocode.ServiceStopping.Code {
		t.Fatalf("expected ServiceStopping, got %s", recorder.Body)
	}
}
