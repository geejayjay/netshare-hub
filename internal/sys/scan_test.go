package sys

import (
	"context"
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
)

func TestWalkShareFSRules(t *testing.T) {
	fsys := fstest.MapFS{
		"docs/a.txt":            {Data: []byte("a")},
		"docs/link":             {Data: []byte("docs/a.txt"), Mode: fs.ModeSymlink},
		"dirlink":               {Data: []byte("docs"), Mode: fs.ModeSymlink},
		".recycle/old.txt":      {Data: []byte("old")},
		".nsh-uploads/part.bin": {Data: []byte("p")},
		"sub/.Trash/x":          {Data: []byte("x")},
		"pipe":                  {Mode: fs.ModeNamedPipe},
	}
	var got []string
	if err := WalkShareFS(context.Background(), fsys, func(e ShareEntry) error {
		got = append(got, e.Path)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := []string{"docs", "docs/a.txt", "sub"}; !slices.Equal(got, want) {
		t.Errorf("walk = %v, want %v", got, want)
	}

	files, err := StatShareFS(context.Background(), fsys, []string{"docs/a.txt", "docs/link", ".recycle/old.txt", "../x", "", "pipe"}, true)
	if err != nil {
		t.Fatal(err)
	}
	// sha256("a")
	if len(files) != 1 || files[0].SHA256 != "ca978112ca1bbdcafac231b39a23dc4da786eff8147c4e72b9807785afee48bb" {
		t.Errorf("stat = %+v, want only docs/a.txt with its hash", files)
	}
}
