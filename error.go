package link

import "errors"

var (
	ErrManagerClosed = errors.New("manager is closed")
	ErrLinkClosed    = errors.New("link is closed")
)
