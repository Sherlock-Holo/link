package link

import (
	"context"
	"io/ioutil"
	"net"
	"sync"
	"testing"

	"github.com/akutz/memconn"
	"golang.org/x/xerrors"
)

func initTest(t *testing.T, addr string) (client net.Conn, server net.Conn) {
	listener, err := memconn.Listen("memb", addr)
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("listen memconn failed: %w", err))
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		var err error
		client, err = memconn.Dial("memb", addr)
		if err != nil {
			t.Fatalf("%+v", xerrors.Errorf("menconn dial failed: %w", err))
		}
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		var err error
		server, err = listener.Accept()
		if err != nil {
			t.Fatalf("%+v", xerrors.Errorf("menconn listener accept failed: %w", err))
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
	// clientCfg.DebugLog = true
	serverCfg := DefaultConfig(ServerMode)
	// serverCfg.DebugLog = true

	clientManager := NewManager(client, clientCfg)
	serverManager := NewManager(server, serverCfg)

	fileB, err := ioutil.ReadFile("testdata/more-than-65535")
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("read fileB failed: %w", err))
	}

	go func() {
		link, err := clientManager.Dial(context.Background())
		if err != nil {
			t.Fatalf("%+v", xerrors.Errorf("client dial failed: %w", err))
		}
		defer link.Close()

		if _, err := link.Write(fileB); err != nil {
			t.Fatalf("%+v", xerrors.Errorf("client write fileB failed: %w", err))
		}
	}()

	link, err := serverManager.Accept()
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("server accept failed: %w", err))
	}
	defer link.Close()

	b, err := ioutil.ReadAll(link)
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("server read b failed: %w", err))
	}

	if string(b) != string(fileB) {
		t.Errorf("data verify failed, receive:\n%s\n\n\n\n\nwant:\n%s", string(b), string(fileB))
	}
}

func TestLinkServerToClient(t *testing.T) {
	client, server := initTest(t, "server to client")
	defer func() {
		client.Close()
		server.Close()
	}()

	clientCfg := DefaultConfig(ClientMode)
	serverCfg := DefaultConfig(ServerMode)

	clientManager := NewManager(client, clientCfg)
	serverManager := NewManager(server, serverCfg)

	fileB, err := ioutil.ReadFile("testdata/more-than-65535")
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("read fileB failed: %w", err))
	}

	go func() {
		link, err := serverManager.Accept()
		if err != nil {
			t.Fatalf("%+v", xerrors.Errorf("server accept failed: %w", err))
		}
		defer link.Close()

		if _, err := link.Write(fileB); err != nil {
			t.Fatalf("%+v", xerrors.Errorf("server write fileB failed: %w", err))
		}

	}()

	link, err := clientManager.Dial(context.Background())
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("client dial failed: %w", err))
	}
	defer link.Close()

	b, err := ioutil.ReadAll(link)
	if err != nil {
		t.Fatalf("%+v", xerrors.Errorf("client read b failed: %w", err))
	}

	if string(b) != string(fileB) {
		t.Errorf("data verify failed, receive:\n%s\n want:\n%s", string(b), string(fileB))
	}
}
