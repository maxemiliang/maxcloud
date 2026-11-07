package objectstorage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// GetObject reads at most MaxObjectBytes, whether or not the server says
// how big the object is, and fails rather than return part of it.
func TestGetObjectLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Path style: /<bucket>/<size>[-chunked]
		key := strings.TrimPrefix(r.URL.Path, "/b/")
		sizeStr, chunked := strings.CutSuffix(key, "-chunked")
		size, err := strconv.Atoi(sizeStr)
		if err != nil {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		if !chunked {
			w.Header().Set("Content-Length", strconv.Itoa(size))
		}
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok && chunked {
			f.Flush() // no Content-Length from here on
		}
		io.CopyN(w, zeros{}, int64(size))
	}))
	t.Cleanup(srv.Close)
	s := NewS3(srv.URL, "europe-1", "id", "secret")

	for _, tc := range []struct {
		key      string
		wantSize int
		tooLarge bool
	}{
		{key: "10", wantSize: 10},
		{key: strconv.Itoa(MaxObjectBytes), wantSize: MaxObjectBytes},
		{key: strconv.Itoa(MaxObjectBytes) + "-chunked", wantSize: MaxObjectBytes},
		{key: strconv.Itoa(MaxObjectBytes + 1), tooLarge: true},
		{key: strconv.Itoa(MaxObjectBytes+1) + "-chunked", tooLarge: true},
		{key: strconv.Itoa(64<<20) + "-chunked", tooLarge: true},
	} {
		data, found, err := s.GetObject(context.Background(), "b", tc.key)
		if tc.tooLarge {
			if !errors.Is(err, ErrTooLarge) || data != nil || found {
				t.Errorf("%s: GetObject = %d bytes, %v, %v; want ErrTooLarge", tc.key, len(data), found, err)
			}
			continue
		}
		if err != nil || !found || len(data) != tc.wantSize || !bytes.Equal(data, make([]byte, tc.wantSize)) {
			t.Errorf("%s: GetObject = %d bytes, %v, %v; want %d bytes", tc.key, len(data), found, err, tc.wantSize)
		}
	}
}

type zeros struct{}

func (zeros) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
