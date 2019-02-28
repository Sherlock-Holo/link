package link

import (
	"context"
	"io/ioutil"
	"net"
	"sync"
	"testing"

	"github.com/akutz/memconn"
	"github.com/pkg/errors"
)

func initTest(t *testing.T, addr string) (client net.Conn, server net.Conn) {
	listener, err := memconn.Listen("memb", addr)
	if err != nil {
		t.Fatalf("listen memconn failed: %+v", errors.WithStack(err))
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		var err error
		client, err = memconn.Dial("memb", addr)
		if err != nil {
			t.Fatalf("menconn dial failed: %+v", errors.WithStack(err))
		}
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		var err error
		server, err = listener.Accept()
		if err != nil {
			t.Fatalf("menconn listener accept failed: %+v", errors.WithStack(err))
		}
		wg.Done()
	}()

	wg.Wait()

	return
}

func TestLinkClientToServer(t *testing.T) {
	client, server := initTest(t, "client to server")
	defer func() {
		client.Close()
		server.Close()
	}()

	clientCfg := DefaultConfig(ClientMode)
	clientCfg.DebugLog = false

	serverCfg := DefaultConfig(ServerMode)
	serverCfg.DebugLog = false

	clientManager := NewManager(client, clientCfg)
	serverManager := NewManager(server, serverCfg)

	fileB, err := ioutil.ReadFile("testdata/more-than-65535")
	if err != nil {
		t.Fatalf("read fileB failed: %+v", errors.WithStack(err))
	}

	go func() {
		link, err := clientManager.Dial(context.Background())
		if err != nil {
			t.Fatalf("client dial failed: %+v", err)
		}
		defer link.Close()

		if _, err := link.Write(fileB); err != nil {
			t.Fatalf("client write fileB failed: %+v", err)
		}
	}()

	link, err := serverManager.Accept()
	if err != nil {
		t.Fatalf("server accept failed: %+v", err)
	}
	defer link.Close()

	b, err := ioutil.ReadAll(link)
	if err != nil {
		t.Fatalf("server read b failed: %+v", errors.WithStack(err))
	}

	if string(b) != string(fileB) {
		t.Fatalf("data verify failed, receive:\n%s\n want:\n%s", string(b), string(fileB))
	}
}

func TestLinkServerToClient(t *testing.T) {
	client, server := initTest(t, "server to client")
	defer func() {
		client.Close()
		server.Close()
	}()

	clientCfg := DefaultConfig(ClientMode)
	clientCfg.DebugLog = false

	serverCfg := DefaultConfig(ServerMode)
	serverCfg.DebugLog = false

	clientManager := NewManager(client, clientCfg)
	serverManager := NewManager(server, serverCfg)

	fileB, err := ioutil.ReadFile("testdata/more-than-65535")
	if err != nil {
		t.Fatalf("read fileB failed: %+v", errors.WithStack(err))
	}

	go func() {
		link, err := serverManager.Accept()
		if err != nil {
			t.Fatalf("server accept failed: %+v", err)
		}
		defer link.Close()

		// need to run before Write, read the byte which client written
		if _, err := link.Read(make([]byte, 1)); err != nil {
			t.Fatalf("server read init data failed: %+v", err)
		}

		if _, err := link.Write(fileB); err != nil {
			t.Fatalf("server write fileB failed: %+v", err)
		}

	}()

	link, err := clientManager.Dial(context.Background())
	if err != nil {
		t.Fatalf("client dial failed: %+v", err)
	}
	defer link.Close()

	// need to run before ReadAll, ensure server know client wants to create a link
	if _, err := link.Write([]byte{1}); err != nil {
		t.Fatalf("client write init data failed: %+v", err)
	}

	b, err := ioutil.ReadAll(link)
	if err != nil {
		t.Fatalf("client read b failed: %+v", errors.WithStack(err))
	}

	if string(b) != string(fileB) {
		t.Fatalf("data verify failed, receive:\n%s\n want:\n%s", string(b), string(fileB))
	}
}
