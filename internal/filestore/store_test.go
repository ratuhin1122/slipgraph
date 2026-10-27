package filestore

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"io"
	"strings"
	"testing"
)

func TestLocalScope(t *testing.T) {
	s := Local{Root: t.TempDir()}
	a, b, id := uuid.New(), uuid.New(), uuid.New()
	key, e := s.Put(context.Background(), a, id, strings.NewReader("recording"))
	if e != nil {
		t.Fatal(e)
	}
	if key != a.String()+"/"+id.String()+".bin" {
		t.Fatal("key not randomized and scoped")
	}
	f, e := s.Open(context.Background(), a, key)
	if e != nil {
		t.Fatal(e)
	}
	data, e := io.ReadAll(f)
	_ = f.Close()
	if e != nil || string(data) != "recording" {
		t.Fatal("wrong stored content")
	}
	for _, key := range []string{key, "../outside", a.String() + "/../../outside", a.String() + "/name.html", "/etc/passwd"} {
		if _, e := s.Open(context.Background(), b, key); !errors.Is(e, ErrScope) {
			t.Fatal("scope or traversal accepted")
		}
		if e := s.Delete(context.Background(), b, key); !errors.Is(e, ErrScope) {
			t.Fatal("foreign deletion accepted")
		}
	}
	if e := s.Delete(context.Background(), a, key); e != nil {
		t.Fatal(e)
	}
}
func TestCancelledWriteLeavesNoFile(t *testing.T) {
	s := Local{Root: t.TempDir()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a, id := uuid.New(), uuid.New()
	if _, e := s.Put(ctx, a, id, strings.NewReader("data")); e == nil {
		t.Fatal("cancelled write accepted")
	}
	if _, e := s.Open(context.Background(), a, a.String()+"/"+id.String()+".bin"); e == nil {
		t.Fatal("partial file survived")
	}
}
