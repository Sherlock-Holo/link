package internal

import "time"

// Config manager config.
type Config struct {
	AcceptQueueSize   int
	KeepaliveInterval time.Duration
	BufferSize        int
}

// KeepaliveConfig DefaultConfig enable keepalive.
var KeepaliveConfig = &Config{
	AcceptQueueSize:   1000,
	KeepaliveInterval: 15 * time.Second,
	BufferSize:        65535,
}

// DefaultConfig default config.
var DefaultConfig = &Config{
	AcceptQueueSize:   1000,
	KeepaliveInterval: 0,
	BufferSize:        65535,
}
