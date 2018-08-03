package link

import "sync"

var bytesPool = sync.Pool{
	New: func() interface{} {
		return make([]byte, HeaderLength)
	},
}

func getBytes(length int) (b []byte) {
	b = bytesPool.Get().([]byte)

	for len(b) < length {
		b = append(b, bytesPool.Get().([]byte)...)
	}

	return b[:length]
}

func releaseBytes(b []byte) {
	bytesPool.Put(b)
}
