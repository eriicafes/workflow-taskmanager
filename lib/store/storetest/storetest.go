// Package storetest provides Stores on an in-memory Redis for tests.
package storetest

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/eriicafes/workflow-taskmanager/lib/store"
)

// New returns a Store on an in-memory Redis that lives as long as t.
func New(t testing.TB) *store.Store {
	t.Helper()
	server := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { rdb.Close() })
	return store.New(rdb, "taskmanager-test")
}
