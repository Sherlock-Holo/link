package link

import "time"

type Config struct {
	WriteRequests     int
	AcceptQueueSize   int
	KeepaliveInterval time.Duration
}

var DefaultConfig = &Config{
	WriteRequests:     1000,
	AcceptQueueSize:   1000,
	KeepaliveInterval: 30 * time.Second,
}
