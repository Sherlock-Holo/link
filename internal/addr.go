package internal

type Addr struct {
	ID string
}

func (a Addr) Network() string {
	return "link"
}

func (a Addr) String() string {
	return a.ID
}
