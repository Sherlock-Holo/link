package link

import (
	"io"
	"log"
	"net"
	"os"
	"testing"
	"time"
)

func TestKeepalive(t *testing.T) {
	go keepaliveServer(t)
	time.Sleep(time.Second)
	keepaliveClient(t)
}

func keepaliveClient(t *testing.T) {
	conn, err := net.Dial("tcp", "127.0.0.1:9876")
	if err != nil {
		t.Error(err)
	}

	manager := NewManager(conn, nil)

	link, err := manager.NewLink()
	if err != nil {
		t.Error(err)
	}

	if _, err = io.WriteString(link, "hello~"); err != nil {
		t.Error(err)
	}

	time.Sleep(100 * time.Second)
}

func keepaliveServer(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:9876")
	if err != nil {
		t.Error(err)
	}

	conn, err := listener.Accept()
	if err != nil {
		t.Error(err)
	}

	manager := NewManager(conn, nil)

	link, err := manager.Accept()
	if err != nil {
		log.Fatal(err)
	}

	io.Copy(os.Stdout, link)
}
