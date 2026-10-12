package web

import (
	"io/fs"
	"os"
	"testing"
)

// Every file Vite writes to dist must be embedded: plain //go:embed silently
// skips names starting with "." or "_".
func TestEveryDistFileIsEmbedded(t *testing.T) {
	disk := os.DirFS("dist")
	embedded := FS()
	err := fs.WalkDir(disk, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if _, err := fs.Stat(embedded, p); err != nil {
			t.Errorf("dist/%s is not embedded: %v", p, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fs.Stat(embedded, "index.html"); err != nil {
		t.Fatal("index.html missing from the embed")
	}
}
