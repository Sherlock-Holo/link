package link

import "time"

type Config struct {
	// Deprecated
	WriteRequests     int
	AcceptQueueSize   int
	KeepaliveInterval time.Duration
}

var KeepaliveConfig = &Config{
	WriteRequests:     1000,
	AcceptQueueSize:   1000,
	KeepaliveInterval: 15 * time.Second,
}

var DefaultConfig = &Config{
	WriteRequests:     1000,
	AcceptQueueSize:   1000,
	KeepaliveInterval: 0,
}
