package link

import (
	"fmt"

	"github.com/pkg/errors"
)

var (
	ErrManagerClosed = errors.New("manager is closed")
	ErrLinkClosed    = errors.New("link is closed")
	ErrTimeout       = errors.New("io timeout")
)

type ErrVersion struct {
	Receive     uint8
	NeedVersion uint8
}

type ErrCmd struct {
	Receive uint8
}

func (ec ErrCmd) Error() string {
	return fmt.Sprintf("receive error cmd %d", ec.Receive)
}

func (ev ErrVersion) Error() string {
	return fmt.Sprintf("receive error version %d, right version is %d", ev.Receive, ev.NeedVersion)
}
