package filesystem

import (
	"compress/gzip"
	"context"
	"fmt"
	"io"
	iofs "io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	. "github.com/franela/goblin"
	"github.com/mholt/archives"
)

func TestArchive_Stream(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Archive", func() {
		g.AfterEach(func() {
			// Reset the filesystem after each run.
			_ = fs.TruncateRootDirectory()
		})

		g.It("creates archive with intended files", func() {
			g.Assert(fs.CreateDirectory("test", "/")).IsNil()
			g.Assert(fs.CreateDirectory("test2", "/")).IsNil()

			r := strings.NewReader("hello, world!\n")
			err := fs.Write("test/file.txt", r, r.Size(), 0o644)
			g.Assert(err).IsNil()

			r = strings.NewReader("hello, world!\n")
			err = fs.Write("test2/file.txt", r, r.Size(), 0o644)
			g.Assert(err).IsNil()

			r = strings.NewReader("hello, world!\n")
			err = fs.Write("test_file.txt", r, r.Size(), 0o644)
			g.Assert(err).IsNil()

			r = strings.NewReader("hello, world!\n")
			err = fs.Write("test_file.txt.old", r, r.Size(), 0o644)
			g.Assert(err).IsNil()

			a := &Archive{
				Filesystem: fs,
				Files: []string{
					"test",
					"test_file.txt",
				},
			}

			// Create the archive.
			archivePath := filepath.Join(rfs.root, "archive.tar.gz")
			g.Assert(a.Create(context.Background(), archivePath)).IsNil()

			// Ensure the archive exists.
			_, err = os.Stat(archivePath)
			g.Assert(err).IsNil()

			// Open the archive.
			genericFs, err := archives.FileSystem(context.Background(), archivePath, nil)
			g.Assert(err).IsNil()

			// Assert that we are opening an archive.
			afs, ok := genericFs.(iofs.ReadDirFS)
			g.Assert(ok).IsTrue()

			// Get the names of the files recursively from the archive.
			files, err := getFiles(afs, ".")
			g.Assert(err).IsNil()

			// Ensure the files in the archive match what we are expecting.
			expected := []string{
				"test_file.txt",
				"test/file.txt",
			}

			// Sort the slices to ensure the comparison never fails if the
			// contents are sorted differently.
			sort.Strings(expected)
			sort.Strings(files)

			g.Assert(files).Equal(expected)
		})

		g.It("treats regular expression metacharacters in ignore patterns literally", func() {
			g.Assert(fs.CreateDirectory("world (copy)", "/")).IsNil()
			g.Assert(fs.CreateDirectory("logs", "/")).IsNil()
			for _, name := range []string{"world (copy)/level.dat", "logs/latest.log", "server.jar"} {
				r := strings.NewReader("hello, world!\n")
				g.Assert(fs.Write(name, r, r.Size(), 0o644)).IsNil()
			}

			a := &Archive{
				Filesystem: fs,
				Ignore:     "world (copy)/\n*.log\n",
			}

			archivePath := filepath.Join(rfs.root, "archive.tar.gz")
			g.Assert(a.Create(context.Background(), archivePath)).IsNil()

			genericFs, err := archives.FileSystem(context.Background(), archivePath, nil)
			g.Assert(err).IsNil()
			afs, ok := genericFs.(iofs.ReadDirFS)
			g.Assert(ok).IsTrue()

			files, err := getFiles(afs, ".")
			g.Assert(err).IsNil()
			g.Assert(files).Equal([]string{"server.jar"})
		})

		g.It("rejects an ignore list that exceeds the limits", func() {
			r := strings.NewReader("hello, world!\n")
			g.Assert(fs.Write("server.jar", r, r.Size(), 0o644)).IsNil()

			a := &Archive{
				Filesystem: fs,
				Ignore:     strings.Repeat("*a", MaxIgnorePatternWildcards+1),
			}

			archivePath := filepath.Join(rfs.root, "archive.tar.gz")
			g.Assert(a.Create(context.Background(), archivePath)).IsNotNil()
		})
	})
}

// rawArchiveEntries returns the typeflag and name of every header written to the
// given tar.gz archive, reading the blocks as they are stored on disk rather
// than going through a reader that understands PAX extension records.
func rawArchiveEntries(t *testing.T, archivePath string) [][2]string {
	t.Helper()

	f, err := os.Open(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()

	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatal(err)
	}

	var entries [][2]string
	for off := 0; off+512 <= len(data); {
		blk := data[off : off+512]
		if blk[0] == 0 {
			break
		}

		name := blk[0:100]
		if i := strings.IndexByte(string(name), 0); i >= 0 {
			name = name[:i]
		}
		var size int
		if _, err := fmt.Sscanf(strings.Trim(string(blk[124:136]), "\x00 "), "%o", &size); err != nil {
			t.Fatalf("failed to parse size field: %s", err)
		}
		entries = append(entries, [2]string{string(blk[156:157]), string(name)})

		off += 512 + (size+511)/512*512
	}
	return entries
}

// TestArchive_Stream_NonASCIINames ensures names containing characters outside
// of ASCII are stored in the tar header itself rather than only inside PAX
// extension records. Tools that do not understand PAX (WinRAR, Bandizip, ...)
// otherwise show the placeholder name from the main header, which has all of
// the non-ASCII characters removed.
func TestArchive_Stream_NonASCIINames(t *testing.T) {
	fs, rfs := NewFs()
	t.Cleanup(func() {
		_ = fs.TruncateRootDirectory()
	})

	if err := fs.CreateDirectory("文件夹", "/"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"中文.jar", "文件夹/测试.txt"} {
		r := strings.NewReader("hello, world!\n")
		if err := fs.Write(name, r, r.Size(), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	a := &Archive{Filesystem: fs}
	archivePath := filepath.Join(rfs.root, "archive.tar.gz")
	if err := a.Create(context.Background(), archivePath); err != nil {
		t.Fatal(err)
	}

	entries := rawArchiveEntries(t, archivePath)

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		switch e[0] {
		case "x", "g":
			t.Fatalf("archive contains a PAX extension header for %q, names must be stored directly in the header", e[1])
		}
		names = append(names, e[1])
	}

	sort.Strings(names)
	expected := []string{"中文.jar", "文件夹/测试.txt"}
	if !reflect.DeepEqual(names, expected) {
		t.Fatalf("expected entry names %q, got %q", expected, names)
	}
}

func getFiles(f iofs.ReadDirFS, name string) ([]string, error) {
	var v []string

	entries, err := f.ReadDir(name)
	if err != nil {
		return nil, err
	}

	for _, e := range entries {
		entryName := e.Name()
		if name != "." {
			entryName = filepath.Join(name, entryName)
		}

		if e.IsDir() {
			files, err := getFiles(f, entryName)
			if err != nil {
				return nil, err
			}

			if files == nil {
				return nil, nil
			}

			v = append(v, files...)
			continue
		}

		v = append(v, entryName)
	}

	return v, nil
}
