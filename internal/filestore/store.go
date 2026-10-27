// Package filestore stores opaque, workspace-prefixed media outside the web root.
package filestore

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var ErrScope = errors.New("invalid storage scope")

type File interface {
	io.Reader
	io.Seeker
	io.Closer
}
type Store interface {
	Put(context.Context, uuid.UUID, uuid.UUID, io.Reader) (string, error)
	Open(context.Context, uuid.UUID, string) (File, error)
	Delete(context.Context, uuid.UUID, string) error
}
type Local struct{ Root string }

func (l Local) validate(w uuid.UUID, key string) error {
	parts := strings.Split(key, "/")
	if w == uuid.Nil || len(parts) != 2 || parts[0] != w.String() || !filepath.IsAbs(l.Root) {
		return ErrScope
	}
	id, e := uuid.Parse(strings.TrimSuffix(parts[1], ".bin"))
	if e != nil || id == uuid.Nil || parts[1] != id.String()+".bin" {
		return ErrScope
	}
	return nil
}
func (l Local) workspace(w uuid.UUID, create bool) (*os.Root, error) {
	root, e := os.OpenRoot(l.Root)
	if e != nil {
		return nil, e
	}
	defer func() { _ = root.Close() }()
	if create {
		if e := root.Mkdir(w.String(), 0700); e != nil && !errors.Is(e, os.ErrExist) {
			return nil, e
		}
	}
	info, e := root.Lstat(w.String())
	if e != nil {
		return nil, e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrScope
	}
	return root.OpenRoot(w.String())
}
func (l Local) Put(ctx context.Context, w, id uuid.UUID, r io.Reader) (string, error) {
	key := w.String() + "/" + id.String() + ".bin"
	if e := l.validate(w, key); e != nil {
		return "", e
	}
	if e := os.MkdirAll(l.Root, 0700); e != nil {
		return "", e
	}
	root, e := l.workspace(w, true)
	if e != nil {
		return "", e
	}
	defer func() { _ = root.Close() }()
	name := id.String() + ".bin"
	f, e := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return "", e
	}
	_, e = io.Copy(f, &contextReader{ctx: ctx, r: r})
	closeErr := f.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		_ = root.Remove(name)
		return "", e
	}
	return key, nil
}
func (l Local) Open(ctx context.Context, w uuid.UUID, key string) (File, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if e := l.validate(w, key); e != nil {
		return nil, e
	}
	root, e := l.workspace(w, false)
	if e != nil {
		return nil, e
	}
	defer func() { _ = root.Close() }()
	return root.Open(strings.TrimPrefix(key, w.String()+"/"))
}
func (l Local) Delete(ctx context.Context, w uuid.UUID, key string) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := l.validate(w, key); e != nil {
		return e
	}
	root, e := l.workspace(w, false)
	if e != nil {
		return e
	}
	defer func() { _ = root.Close() }()
	return root.Remove(strings.TrimPrefix(key, w.String()+"/"))
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *contextReader) Read(b []byte) (int, error) {
	if e := r.ctx.Err(); e != nil {
		return 0, e
	}
	return r.r.Read(b)
}
