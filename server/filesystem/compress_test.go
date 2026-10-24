package filesystem

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	. "github.com/franela/goblin"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// Given an archive named test.{ext}, with the following file structure:
//
//	test/
//	|──inside/
//	|────finside.txt
//	|──outside.txt
//
// this test will ensure that it's being decompressed as expected
func TestFilesystem_DecompressFile(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Decompress", func() {
		for _, ext := range []string{"zip", "rar", "tar", "tar.gz"} {
			g.It("can decompress a "+ext, func() {
				// copy the file to the new FS
				c, err := os.ReadFile("./testdata/test." + ext)
				g.Assert(err).IsNil()
				err = rfs.CreateServerFile("./test."+ext, c)
				g.Assert(err).IsNil()

				// decompress
				err = fs.DecompressFile(context.Background(), "/", "test."+ext)
				g.Assert(err).IsNil()

				// make sure everything is where it is supposed to be
				_, err = rfs.StatServerFile("test/outside.txt")
				g.Assert(err).IsNil()

				st, err := rfs.StatServerFile("test/inside")
				g.Assert(err).IsNil()
				g.Assert(st.IsDir()).IsTrue()

				_, err = rfs.StatServerFile("test/inside/finside.txt")
				g.Assert(err).IsNil()
				g.Assert(st.IsDir()).IsTrue()
			})
		}

		g.AfterEach(func() {
			_ = fs.TruncateRootDirectory()
		})
	})
}

// Test for GBK-encoded filenames in archives
func TestFilesystem_DecompressFile_GBK(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Decompress GBK-encoded filenames", func() {
		g.It("can decompress a zip with GBK-encoded filenames", func() {
			// copy the file to the new FS
			c, err := os.ReadFile("./testdata/test-gbk.zip")
			g.Assert(err).IsNil()
			err = rfs.CreateServerFile("./test-gbk.zip", c)
			g.Assert(err).IsNil()

			// decompress
			err = fs.DecompressFile(context.Background(), "/", "test-gbk.zip")
			g.Assert(err).IsNil()

			// make sure the file was extracted with proper UTF-8 filename
			_, err = rfs.StatServerFile("测试文件夹/测试文档.txt")
			g.Assert(err).IsNil()
		})

		g.It("can check space for a zip with GBK-encoded filenames", func() {
			// copy the file to the new FS
			c, err := os.ReadFile("./testdata/test-gbk.zip")
			g.Assert(err).IsNil()
			err = rfs.CreateServerFile("./test-gbk.zip", c)
			g.Assert(err).IsNil()

			// check space availability
			err = fs.SpaceAvailableForDecompression(context.Background(), "/", "test-gbk.zip")
			g.Assert(err).IsNil()
		})

		g.AfterEach(func() {
			_ = fs.TruncateRootDirectory()
		})
	})
}

// Test the decodeFilename helper function
func TestDecodeFilename(t *testing.T) {
	g := Goblin(t)

	g.Describe("decodeFilename", func() {
		g.It("should pass through valid UTF-8 strings", func() {
			input := "test/测试文件.txt"
			output := decodeFilename(input)
			g.Assert(output).Equal(input)
		})

		g.It("should pass through ASCII strings", func() {
			input := "test/file.txt"
			output := decodeFilename(input)
			g.Assert(output).Equal(input)
		})

		g.It("should convert GBK to UTF-8", func() {
			// Create a GBK-encoded string
			utf8String := "测试文件.txt"
			gbkString, err := simplifiedchinese.GBK.NewEncoder().String(utf8String)
			g.Assert(err).IsNil()

			// Verify it's not valid UTF-8
			g.Assert(utf8.ValidString(gbkString)).IsFalse()

			// Decode and verify it matches original UTF-8
			output := decodeFilename(gbkString)
			g.Assert(output).Equal(utf8String)
		})
	})
}

// Empty directories have no file to create them implicitly, so extraction must
// create them explicitly or they are dropped.
func TestFilesystem_DecompressFileEmptyDirectory(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("Decompress", func() {
		archives := []struct {
			name  string
			build func() ([]byte, error)
		}{
			{"empty.zip", zipWithEmptyDir},
			{"empty.tar.gz", tarGzWithEmptyDir},
		}

		for _, a := range archives {
			g.It("preserves an empty directory in a "+a.name, func() {
				content, err := a.build()
				g.Assert(err).IsNil()
				err = rfs.CreateServerFile("./"+a.name, content)
				g.Assert(err).IsNil()

				err = fs.DecompressFile(context.Background(), "/", a.name)
				g.Assert(err).IsNil()

				// The empty directory must exist, and the sibling file must still extract.
				st, err := rfs.StatServerFile("empty")
				g.Assert(err).IsNil()
				g.Assert(st.IsDir()).IsTrue()

				_, err = rfs.StatServerFile("outside.txt")
				g.Assert(err).IsNil()
			})
		}

		g.AfterEach(func() {
			_ = fs.TruncateRootDirectory()
		})
	})
}

// deepArchive builds a zip or tar.gz archive holding the given number of files,
// all nested in the same deep directory.
func deepArchive(format string, entries int, depth int) ([]byte, error) {
	var buf bytes.Buffer
	prefix := strings.Repeat("a/", depth)
	content := []byte("hello")
	if format == "zip" {
		zw := zip.NewWriter(&buf)
		for i := 0; i < entries; i++ {
			w, err := zw.Create(prefix + "f" + strconv.Itoa(i) + ".txt")
			if err != nil {
				return nil, err
			}
			if _, err := w.Write(content); err != nil {
				return nil, err
			}
		}
		if err := zw.Close(); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	for i := 0; i < entries; i++ {
		h := &tar.Header{Name: prefix + "f" + strconv.Itoa(i) + ".txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content)), Format: tar.FormatPAX}
		if err := tw.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := tw.Write(content); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// Checking the size of an archive takes time in proportion to the archive, however
// deeply nested its entries are.
func TestFilesystem_SpaceAvailableForDecompressionDeepArchive(t *testing.T) {
	g := Goblin(t)
	fs, rfs := NewFs()

	g.Describe("SpaceAvailableForDecompression", func() {
		for _, ext := range []string{"zip", "rar", "tar", "tar.gz"} {
			g.It("reads the files in a "+ext, func() {
				c, err := os.ReadFile("./testdata/test." + ext)
				g.Assert(err).IsNil()
				g.Assert(rfs.CreateServerFile("./test."+ext, c)).IsNil()

				fs.SetDiskLimit(1024 * 1024 * 1024)
				g.Assert(fs.SpaceAvailableForDecompression(context.Background(), "/", "test."+ext)).IsNil()
			})
		}

		for _, format := range []string{"zip", "tar.gz"} {
			g.It("rejects a "+format+" holding more than the space available", func() {
				// Ten files of five bytes each.
				b, err := deepArchive(format, 10, 1)
				g.Assert(err).IsNil()
				g.Assert(rfs.CreateServerFile("full."+format, b)).IsNil()

				usage, err := fs.DiskUsage(false)
				g.Assert(err).IsNil()
				fs.SetDiskLimit(usage + 49)
				err = fs.SpaceAvailableForDecompression(context.Background(), "/", "full."+format)
				g.Assert(IsErrorCode(err, ErrCodeDiskSpace)).IsTrue()

				fs.SetDiskLimit(usage + 50)
				g.Assert(fs.SpaceAvailableForDecompression(context.Background(), "/", "full."+format)).IsNil()
			})
		}

		for _, format := range []string{"zip", "tar.gz"} {
			g.It("checks deeply nested entries in a "+format+" in linear time", func() {
				g.Timeout(time.Minute)
				fs.SetDiskLimit(1024 * 1024 * 1024)

				b, err := deepArchive(format, 400, 2000)
				g.Assert(err).IsNil()
				g.Assert(rfs.CreateServerFile("deep."+format, b)).IsNil()

				start := time.Now()
				g.Assert(fs.SpaceAvailableForDecompression(context.Background(), "/", "deep."+format)).IsNil()
				if d := time.Since(start); d > time.Second {
					g.Failf("checking the archive took %s", d)
				}
			})

			g.It("rejects entries in a "+format+" with names that are too long", func() {
				fs.SetDiskLimit(1024 * 1024 * 1024)

				b, err := deepArchive(format, 1, 4000)
				g.Assert(err).IsNil()
				g.Assert(rfs.CreateServerFile("long."+format, b)).IsNil()

				g.Assert(fs.SpaceAvailableForDecompression(context.Background(), "/", "long."+format) == nil).IsFalse()
			})
		}

		g.AfterEach(func() {
			fs.SetDiskLimit(0)
			_ = fs.TruncateRootDirectory()
		})
	})
}

// zipWithEmptyDir builds a zip holding one file and an empty directory ("empty/").
func zipWithEmptyDir() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	dh := &zip.FileHeader{Name: "empty/"}
	dh.SetMode(os.ModeDir | 0o755)
	if _, err := zw.CreateHeader(dh); err != nil {
		return nil, err
	}

	w, err := zw.Create("outside.txt")
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		return nil, err
	}

	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// tarGzWithEmptyDir builds a tar.gz holding one file and an empty directory ("empty/").
func tarGzWithEmptyDir() ([]byte, error) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	if err := tw.WriteHeader(&tar.Header{Name: "empty/", Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
		return nil, err
	}

	content := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "outside.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(content))}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(content); err != nil {
		return nil, err
	}

	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
