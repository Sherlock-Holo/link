package link

import "time"

type mode int

const (
	ClientMode mode = iota
	ServerMode

	defaultReadBufSize  = 64 * 1024
	defaultWriteBufSize = 64 * 1024
)

// Config manager config.
type Config struct {
	AcceptQueueSize   int
	KeepaliveInterval time.Duration // if config mode is ServerMode, KeepaliveInterval will be ignored
	BufferSize        int
	DebugLog          bool // enable debug log
	Mode              mode // manager run mode
	ReadBufSize       int32
	WriteBufSize      int32
}

// DefaultConfig default config.
func DefaultConfig(mode mode) Config {
	return Config{
		AcceptQueueSize: 1000,
		BufferSize:      65535,
		DebugLog:        false,
		Mode:            mode,
		ReadBufSize:     defaultReadBufSize,
		WriteBufSize:    defaultWriteBufSize,
	}
}

// KeepaliveConfig DefaultConfig enable keepalive.
func KeepaliveConfig(mode mode) Config {
	config := DefaultConfig(mode)
	config.KeepaliveInterval = 15 * time.Second
	return config
}
