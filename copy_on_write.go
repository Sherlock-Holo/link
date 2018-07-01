package link

import (
	"sync"
	"sync/atomic"
)

type cowMap struct {
	m  atomic.Value
	mu sync.Mutex
}

func (m *cowMap) Get(id uint32) (*Link, bool) {
	mMap := m.m.Load().(map[uint32]*Link)

	link, ok := mMap[id]

	return link, ok
}

func (m *cowMap) Set(id uint32, link *Link) {
	m.mu.Lock()

	newmMap := make(map[uint32]*Link)

	oldMap := m.m.Load().(map[uint32]*Link)

	for k, v := range oldMap {
		newmMap[k] = v
	}

	newmMap[id] = link

	m.m.Store(newmMap)

	m.mu.Unlock()
}

func (m *cowMap) Delete(ids ...uint32) {
	m.mu.Lock()

	newmMap := make(map[uint32]*Link)

	oldMap := m.m.Load().(map[uint32]*Link)

	for k, v := range oldMap {
		newmMap[k] = v
	}

	for _, id := range ids {
		delete(newmMap, id)
	}

	m.m.Store(newmMap)

	m.mu.Unlock()
}

func (m *cowMap) Range(f func(id uint32, link *Link)) {
	mMap := m.m.Load().(map[uint32]*Link)

	for key, link := range mMap {
		f(key, link)
	}
}
