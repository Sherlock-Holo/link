package link

import (
	"errors"
	"fmt"
)

var (
	ErrManagerClosed = errors.New("manager is closed")
	ErrLinkClosed    = errors.New("link is closed")
)

type ErrVersion struct {
	Receive     uint8
	NeedVersion uint8
}

func (ev ErrVersion) Error() string {
	return fmt.Sprintf("receive error version %d, right version is %d", ev.Receive, ev.NeedVersion)
}
