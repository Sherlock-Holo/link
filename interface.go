package link

import (
	"context"
	"io"
	"net"
)

type Manager interface {
	Dial(ctx context.Context) (link Link, err error)
	DialData(ctx context.Context, b []byte) (link Link, err error)
	Accept() (link Link, err error)
	IsClosed() bool
	io.Closer
}

type Link interface {
	net.Conn
}
