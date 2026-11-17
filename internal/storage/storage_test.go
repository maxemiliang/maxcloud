package storage

import "testing"

func TestParseRef(t *testing.T) {
	for in, want := range map[string]Ref{
		"uploads:img/cat.png": {"uploads", "img/cat.png"},
		"uploads:":            {"uploads", ""},
		"uploads:/img/":       {"uploads", "img/"},
		"user-files:a:b":      {"user-files", "a:b"},
	} {
		got, ok := ParseRef(in)
		if !ok || got != want {
			t.Errorf("ParseRef(%q) = %+v, %v; want %+v", in, got, ok, want)
		}
	}
	for _, in := range []string{"logo.png", "./a:b", "/tmp/a:b", `C:\x`, "Uploads:a", ":a", "dist/", "-"} {
		if got, ok := ParseRef(in); ok {
			t.Errorf("ParseRef(%q) = %+v: want a local path", in, got)
		}
	}
}

func TestJoin(t *testing.T) {
	for _, c := range []struct{ key, rel, want string }{
		{"", "a/b.txt", "a/b.txt"},
		{"www/", "a/b.txt", "www/a/b.txt"},
		{"www", "a/b.txt", "www/a/b.txt"},
	} {
		if got := (Ref{Bucket: "x", Key: c.key}).Join(c.rel); got != c.want {
			t.Errorf("Join(%q, %q) = %q, want %q", c.key, c.rel, got, c.want)
		}
	}
}

func TestBucketName(t *testing.T) {
	c := &Client{App: "shop"}
	if got := c.Bucket("uploads"); got != "mc-shop--uploads" {
		t.Errorf("Bucket = %q", got)
	}
}
