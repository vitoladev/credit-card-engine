package rules

import "engine/internal/domain"

// Handler is one link in the chain. Handle denies or forwards to the next link.
type Handler interface {
	Name() string
	SetNext(Handler) Handler
	Handle(c domain.Customer) (ok bool, reason string)
}

type link struct {
	next Handler
}

func (l *link) SetNext(h Handler) Handler {
	l.next = h
	return h
}

func (l *link) forward(c domain.Customer) (bool, string) {
	if l.next == nil {
		return true, ""
	}
	return l.next.Handle(c)
}
