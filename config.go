package link

import "time"

// Config manager config.
type Config struct {
	AcceptQueueSize   int
	KeepaliveInterval time.Duration
	BufferSize        int
	EnableLog         bool
}

// DefaultConfig default config.
func DefaultConfig() *Config {
	return &Config{
		AcceptQueueSize: 1000,
		BufferSize:      65535,
		EnableLog:       true,
	}
}

// KeepaliveConfig DefaultConfig enable keepalive.
func KeepaliveConfig() *Config {
	return &Config{
		AcceptQueueSize:   1000,
		KeepaliveInterval: 15 * time.Second,
		BufferSize:        65535,
		EnableLog:         true,
	}
}
