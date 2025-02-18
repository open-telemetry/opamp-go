package internal

type Counter interface {
	Add(int64)
}

type RingBuffer[T any] interface {
	Insert(T)
}
