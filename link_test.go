package link

import (
	"io/ioutil"
	"net"
	"sync"
	"testing"

	"github.com/akutz/memconn"
)

func initTest(t *testing.T, addr string) (client net.Conn, server net.Conn) {
	listener, err := memconn.Listen("memb", addr)
	if err != nil {
		t.Fatal(err)
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		var err error
		client, err = memconn.Dial("memb", addr)
		if err != nil {
			t.Fatal(err)
		}
		wg.Done()
	}()

	wg.Add(1)
	go func() {
		var err error
		server, err = listener.Accept()
		if err != nil {
			t.Fatal(err)
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

	config := DefaultConfig()
	clientManager := NewManager(client, config)
	serverManager := NewManager(server, nil)

	fileB, err := ioutil.ReadFile("testdata/more-than-65535")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		link, err := clientManager.Dial()
		if err != nil {
			t.Fatal(err)
		}
		defer link.Close()

		if _, err := link.Write(fileB); err != nil {
			t.Fatal(err)
		}
	}()

	link, err := serverManager.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()

	b, err := ioutil.ReadAll(link)
	if err != nil {
		t.Fatal(err)
	}

	if string(b) != string(fileB) {
		t.Log("failed")
	}
}

func TestLinkServerToClient(t *testing.T) {
	client, server := initTest(t, "server to client")
	defer func() {
		client.Close()
		server.Close()
	}()

	config := DefaultConfig()
	clientManager := NewManager(client, config)
	serverManager := NewManager(server, nil)

	fileB, err := ioutil.ReadFile("testdata/more-than-65535")
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		link, err := serverManager.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer link.Close()

		// need to run before Write, read the byte which client written
		if _, err := link.Read(make([]byte, 1)); err != nil {
			t.Fatal(err)
		}

		if _, err := link.Write(fileB); err != nil {
			t.Fatal(err)
		}

	}()

	link, err := clientManager.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer link.Close()

	// need to run before ReadAll, ensure server know client wants to create a link
	if _, err := link.Write([]byte{1}); err != nil {
		t.Fatal(err)
	}

	b, err := ioutil.ReadAll(link)
	if err != nil {
		t.Fatal(err)
	}

	if string(b) != string(fileB) {
		t.Log("failed")
	}
}
