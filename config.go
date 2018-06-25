package link

import "time"

// Config manager config.
type Config struct {
	AcceptQueueSize   int
	KeepaliveInterval time.Duration
}

// KeepaliveConfig DefaultConfig enable keepalive.
var KeepaliveConfig = &Config{
	AcceptQueueSize:   1000,
	KeepaliveInterval: 15 * time.Second,
}

// DefaultConfig default config.
var DefaultConfig = &Config{
	AcceptQueueSize:   1000,
	KeepaliveInterval: 0,
}
