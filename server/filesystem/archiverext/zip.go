package archiverext

import (
	"io"
	"io/fs"
	"time"
	"unicode/utf8"

	"github.com/klauspost/compress/zip"
	"golang.org/x/text/encoding/simplifiedchinese"
)

// ZipFS is a wrapper around zip.Reader that implements fs.FS with
// automatic filename decoding. It handles GBK-encoded filenames
// (common in Chinese Windows systems) by converting them to UTF-8.
type ZipFS struct {
	reader *zip.Reader
	file   io.Closer
}

// NewZipFS creates a new ZipFS from a ReaderAt and size.
func NewZipFS(r io.ReaderAt, size int64) (*ZipFS, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}

	// Keep a reference to the underlying file if it's a Closer
	var closer io.Closer
	if c, ok := r.(io.Closer); ok {
		closer = c
	}

	return &ZipFS{reader: zr, file: closer}, nil
}

// Close closes the underlying file if it's a Closer.
func (z *ZipFS) Close() error {
	if z.file != nil {
		return z.file.Close()
	}
	return nil
}

// Open opens the named file from the ZIP archive.
// It automatically decodes GBK-encoded filenames to UTF-8.
func (z *ZipFS) Open(name string) (fs.File, error) {
	// Try to open with the name as-is first (for UTF-8 filenames)
	var targetFile *zip.File
	for _, f := range z.reader.File {
		if f.Name == name {
			targetFile = f
			break
		}
	}

	// If not found, try to find a file with a matching decoded name
	if targetFile == nil {
		for _, f := range z.reader.File {
			decodedName := decodeZipFilename(f.Name)
			if decodedName == name {
				targetFile = f
				break
			}
		}
	}

	if targetFile == nil {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	rc, err := targetFile.Open()
	if err != nil {
		return nil, err
	}

	return &zipFile{
		ReadCloser: rc,
		file:       targetFile,
	}, nil
}

// ReadDir reads the directory named by name from the ZIP archive.
// It returns directory entries with decoded filenames.
func (z *ZipFS) ReadDir(name string) ([]fs.DirEntry, error) {
	// For zip files, we need to build the directory listing
	// by examining all files and filtering by prefix
	var entries []fs.DirEntry

	// Normalize the directory name
	if name == "." {
		name = ""
	} else if name != "" && name[len(name)-1] != '/' {
		name = name + "/"
	}

	seen := make(map[string]bool)
	for _, f := range z.reader.File {
		decodedName := decodeZipFilename(f.Name)

		// Skip files not in this directory
		if name != "" && !hasPrefix(decodedName, name) {
			continue
		}

		// Get the relative path within this directory
		relPath := decodedName
		if name != "" {
			relPath = decodedName[len(name):]
		}

		// Skip if this is the directory itself
		if relPath == "" {
			continue
		}

		// Extract the first path component
		var entryName string
		idx := indexOf(relPath, '/')
		if idx >= 0 {
			entryName = relPath[:idx]
		} else {
			entryName = relPath
		}

		// Skip duplicates
		if seen[entryName] {
			continue
		}
		seen[entryName] = true

		// Determine if this entry is a directory by checking if there's more path after it
		// or if the original file is marked as a directory
		isDir := idx >= 0 || f.FileInfo().IsDir()

		entries = append(entries, &zipDirEntry{
			name:  entryName,
			isDir: isDir,
			info:  &f.FileHeader,
		})
	}

	return entries, nil
}

// Stat returns file information for the named file from the ZIP archive.
func (z *ZipFS) Stat(name string) (fs.FileInfo, error) {
	if name == "." {
		// Return info for root directory
		return &zipRootInfo{}, nil
	}

	// Try to find the file with decoded name
	for _, f := range z.reader.File {
		decodedName := decodeZipFilename(f.Name)
		if decodedName == name || decodedName == name+"/" {
			return f.FileInfo(), nil
		}
	}

	return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrNotExist}
}

// decodeZipFilename decodes a filename from a ZIP archive, automatically
// detecting and converting GBK-encoded filenames to UTF-8.
func decodeZipFilename(filename string) string {
	// Check if it's already valid UTF-8
	if utf8.ValidString(filename) {
		return filename
	}

	// Not valid UTF-8, try to decode as GBK
	decoded, err := simplifiedchinese.GBK.NewDecoder().String(filename)
	if err != nil {
		// GBK decoding failed, return original
		return filename
	}

	// Successfully decoded from GBK
	return decoded
}

// Helper functions
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// zipFile wraps an io.ReadCloser from a zip.File and implements fs.File
type zipFile struct {
	io.ReadCloser
	file *zip.File
}

func (zf *zipFile) Stat() (fs.FileInfo, error) {
	return zf.file.FileInfo(), nil
}

// zipDirEntry implements fs.DirEntry for ZIP file entries
type zipDirEntry struct {
	name  string
	isDir bool
	info  *zip.FileHeader
}

func (e *zipDirEntry) Name() string               { return e.name }
func (e *zipDirEntry) IsDir() bool                { return e.isDir }
func (e *zipDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e *zipDirEntry) Info() (fs.FileInfo, error) { return e.info.FileInfo(), nil }

// zipRootInfo implements fs.FileInfo for the root directory
type zipRootInfo struct{}

func (i *zipRootInfo) Name() string       { return "." }
func (i *zipRootInfo) Size() int64        { return 0 }
func (i *zipRootInfo) Mode() fs.FileMode  { return fs.ModeDir | 0755 }
func (i *zipRootInfo) ModTime() time.Time { return time.Time{} }
func (i *zipRootInfo) IsDir() bool        { return true }
func (i *zipRootInfo) Sys() any           { return nil }
