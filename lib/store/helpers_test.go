package store

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// NewTest returns a Store on an in-memory Redis that lives as long as t.
func NewTest(t testing.TB) *Store {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return New(rdb, "taskmanager-test")
}
