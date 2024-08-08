package errutil

type Iterator struct {
	// unvisited is a stack structure where the last element
	// is the top of the stack.
	unvisited []error
}

// Iterate will iterate through the error chain in a depth-first order.
func Iterate(err error) *Iterator {
	i := &Iterator{}
	if err != nil {
		i.unvisited = []error{err}
	}
	return i
}

// IsValid returns if this iterator is valid.
func (i *Iterator) IsValid() bool {
	return len(i.unvisited) > 0
}

// Next will retrieve the next error in the chain.
func (i *Iterator) Next() {
	if len(i.unvisited) == 0 {
		return
	}

	err := i.pop()
	switch err := err.(type) {
	case interface{ Unwrap() error }:
		if child := err.Unwrap(); child != nil {
			i.push(child)
		}
	case interface{ Unwrap() []error }:
		children := err.Unwrap()
		if len(children) == 0 {
			// Fast path although unlikely.
			return
		}
		i.push(children...)
	}
}

// Err will return the current error for the Iterator.
// It is only valid to invoke this method if IsValid returns true.
func (i *Iterator) Err() error {
	last := len(i.unvisited) - 1
	return i.unvisited[last]
}

func (i *Iterator) pop() (err error) {
	err, i.unvisited = pop(i.unvisited)
	return err
}

func (i *Iterator) push(elems ...error) {
	i.unvisited = push(i.unvisited, elems...)
}

func pop[T any](s []T) (elem T, rest []T) {
	elem = s[len(s)-1]
	rest = s[:len(s)-1]
	return elem, rest
}

func push[T any](s []T, elems ...T) []T {
	if len(elems) == 1 {
		return append(s, elems...)
	}

	// If there are multiple elements, batch
	// add them and reverse the order so the first
	// element is the last in the array (FIFO).
	i := len(s)
	s = append(s, elems...)
	for j := len(s) - 1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
	return s
}
