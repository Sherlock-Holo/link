package link

import (
	"io"
	"net"
)

type Manager interface {
	Dial() (link Link, err error)
	Accept() (link Link, err error)
	IsClosed() bool
	io.Closer
}

type Link interface {
	net.Conn
}
