package link

import "time"

type mode int

const (
	ClientMode mode = iota
	ServerMode
)

// Config manager config.
type Config struct {
	AcceptQueueSize   int
	KeepaliveInterval time.Duration // if config mode is ServerMode, KeepaliveInterval will be ignored
	BufferSize        int
	DebugLog          bool
	Mode              mode
}

// DefaultConfig default config.
func DefaultConfig(mode mode) Config {
	return Config{
		AcceptQueueSize: 1000,
		BufferSize:      65535,
		DebugLog:        false,
		Mode:            mode,
	}
}

// KeepaliveConfig DefaultConfig enable keepalive.
func KeepaliveConfig(mode mode) Config {
	config := DefaultConfig(mode)
	config.KeepaliveInterval = 15 * time.Second
	return config
}
