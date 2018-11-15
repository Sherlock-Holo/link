package link

import (
	"io/ioutil"
	"net"
	"sync"
	"testing"

	"github.com/akutz/memconn"
)

func initTest(t *testing.T) (client net.Conn, server net.Conn) {
	listener, err := memconn.Listen("memb", "test")
	if err != nil {
		t.Fatal(err)
	}

	wg := sync.WaitGroup{}

	wg.Add(1)
	go func() {
		var err error
		client, err = memconn.Dial("memb", "test")
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

func TestLink(t *testing.T) {
	client, server := initTest(t)

	clientManager := NewManager(client, nil)
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
