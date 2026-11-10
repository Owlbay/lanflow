package app

import "lanflow/internal/seqring"

// ring is a fixed-capacity buffer keeping the newest n items, with a
// sequence number per item for incremental reads (internal/seqring).
type ring[T any] = seqring.Ring[T]

func newRing[T any](capacity int) ring[T] { return seqring.New[T](capacity) }
