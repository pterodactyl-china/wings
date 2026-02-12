package filesystem

import (
	"context"
	"os"
	"testing"
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
