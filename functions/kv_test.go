package functions

import (
	"context"
	"slices"
	"testing"
	"time"
)

func TestKV(t *testing.T) {
	testPool(t) // skips without TEST_DATABASE_URL; sets DATABASE_URL
	ctx := context.Background()
	p := queueName() + ":"

	var got map[string]int
	if ok, err := KVGet(ctx, p+"a", &got); err != nil || ok {
		t.Fatalf("missing key: %v, %v", ok, err)
	}
	if err := KVSet(ctx, p+"a", map[string]int{"n": 1}, 0); err != nil {
		t.Fatal(err)
	}
	if ok, err := KVGet(ctx, p+"a", &got); err != nil || !ok || got["n"] != 1 {
		t.Fatalf("get: %v %v %v", got, ok, err)
	}
	if ok, err := KVDelete(ctx, p+"a"); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := KVDelete(ctx, p+"a"); ok {
		t.Error("deleted twice")
	}

	// Expiry.
	if err := KVSet(ctx, p+"short", 1, 200*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if err := KVSet(ctx, p+"long", 2, time.Hour); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	var n int
	if ok, _ := KVGet(ctx, p+"short", &n); ok {
		t.Error("expired key still readable")
	}

	// Pages, in order.
	for _, k := range []string{"c", "b", "d"} {
		if err := KVSet(ctx, p+"k:"+k, k, 0); err != nil {
			t.Fatal(err)
		}
	}
	keys, next, err := KVList(ctx, p+"k:", "", 2)
	if err != nil || !slices.Equal(keys, []string{p + "k:b", p + "k:c"}) || next == "" {
		t.Fatalf("page 1: %v %q %v", keys, next, err)
	}
	keys, next, err = KVList(ctx, p+"k:", next, 2)
	if err != nil || !slices.Equal(keys, []string{p + "k:d"}) || next != "" {
		t.Fatalf("page 2: %v %q %v", keys, next, err)
	}

	if err := KVSet(ctx, "", 1, 0); err == nil {
		t.Error("empty key accepted")
	}
}
