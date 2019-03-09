package link

import (
	"fmt"

	"golang.org/x/xerrors"
)

var (
	ErrManagerClosed = xerrors.New("manager is closed")
	ErrLinkClosed    = xerrors.New("link is closed")
	ErrTimeout       = xerrors.New("io timeout")
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
