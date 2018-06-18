package link

import "time"

type Config struct {
	WriteRequests   int
	AcceptQueueSize int
	Timeout         time.Duration
}

var DefaultConfig = &Config{
	WriteRequests:   1000,
	AcceptQueueSize: 1000,
	Timeout:         30 * time.Second,
}
