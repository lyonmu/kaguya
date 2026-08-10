package pkg

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	gorilla "github.com/gorilla/websocket"
)

type Option func(*options)

type options struct {
	readBufferSize        int
	writeBufferSize       int
	handshakeTimeout      time.Duration
	enableCompression     bool
	allowCrossOrigin      bool
	readLimit             int64
	heartbeat             bool
	heartbeatInterval     time.Duration
	heartbeatTimeout      time.Duration
	heartbeatWriteTimeout time.Duration
}

func defaultOptions() *options {
	return &options{
		readBufferSize:        4096,
		writeBufferSize:       4096,
		handshakeTimeout:      10 * time.Second,
		enableCompression:     false,
		allowCrossOrigin:      true,
		readLimit:             0,
		heartbeat:             true,
		heartbeatInterval:     30 * time.Second,
		heartbeatTimeout:      60 * time.Second,
		heartbeatWriteTimeout: 10 * time.Second,
	}
}

func WithReadBufferSize(size int) Option {
	return func(o *options) {
		o.readBufferSize = size
	}
}

func WithWriteBufferSize(size int) Option {
	return func(o *options) {
		o.writeBufferSize = size
	}
}

func WithHandshakeTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.handshakeTimeout = timeout
	}
}

func WithCompression(enabled bool) Option {
	return func(o *options) {
		o.enableCompression = enabled
	}
}

func WithCrossOrigin(enabled bool) Option {
	return func(o *options) {
		o.allowCrossOrigin = enabled
	}
}

func WithReadLimit(limit int64) Option {
	return func(o *options) {
		o.readLimit = limit
	}
}

func WithHeartbeat(enabled bool) Option {
	return func(o *options) {
		o.heartbeat = enabled
	}
}

func WithHeartbeatInterval(interval time.Duration) Option {
	return func(o *options) {
		o.heartbeatInterval = interval
	}
}

func WithHeartbeatTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.heartbeatTimeout = timeout
	}
}

func WithHeartbeatWriteTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.heartbeatWriteTimeout = timeout
	}
}

func Upgrade(c *gin.Context, opts ...Option) (*gorilla.Conn, error) {
	o := defaultOptions()

	for _, opt := range opts {
		opt(o)
	}

	if err := validateOptions(o); err != nil {
		return nil, err
	}

	upgrader := gorilla.Upgrader{
		ReadBufferSize:    o.readBufferSize,
		WriteBufferSize:   o.writeBufferSize,
		HandshakeTimeout:  o.handshakeTimeout,
		EnableCompression: o.enableCompression,
	}

	if o.allowCrossOrigin {
		upgrader.CheckOrigin = func(_ *http.Request) bool {
			return true
		}
	}

	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return nil, err
	}

	if o.readLimit > 0 {
		conn.SetReadLimit(o.readLimit)
	}

	if o.heartbeat {
		if err := setupHeartbeat(conn, o); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}

	return conn, nil
}

func setupHeartbeat(conn *gorilla.Conn, o *options) error {
	if err := conn.SetReadDeadline(
		time.Now().Add(o.heartbeatTimeout),
	); err != nil {
		return err
	}

	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(
			time.Now().Add(o.heartbeatTimeout),
		)
	})

	go func() {
		ticker := time.NewTicker(o.heartbeatInterval)
		defer ticker.Stop()

		for range ticker.C {
			err := conn.WriteControl(
				gorilla.PingMessage,
				nil,
				time.Now().Add(o.heartbeatWriteTimeout),
			)
			if err != nil {
				_ = conn.Close()
				return
			}
		}
	}()

	return nil
}

func validateOptions(o *options) error {
	if o.readBufferSize <= 0 {
		return fmt.Errorf("websocket: read buffer size must be greater than 0")
	}

	if o.writeBufferSize <= 0 {
		return fmt.Errorf("websocket: write buffer size must be greater than 0")
	}

	if o.handshakeTimeout <= 0 {
		return fmt.Errorf("websocket: handshake timeout must be greater than 0")
	}

	if o.readLimit < 0 {
		return fmt.Errorf("websocket: read limit must not be negative")
	}

	if o.heartbeat {
		if o.heartbeatInterval <= 0 {
			return fmt.Errorf("websocket: heartbeat interval must be greater than 0")
		}

		if o.heartbeatTimeout <= o.heartbeatInterval {
			return fmt.Errorf(
				"websocket: heartbeat timeout must be greater than heartbeat interval",
			)
		}

		if o.heartbeatWriteTimeout <= 0 {
			return fmt.Errorf(
				"websocket: heartbeat write timeout must be greater than 0",
			)
		}
	}

	return nil
}
