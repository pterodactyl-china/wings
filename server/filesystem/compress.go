package filesystem

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	gzip2 "github.com/klauspost/compress/gzip"
	zip2 "github.com/klauspost/compress/zip"

	"emperror.dev/errors"
	"github.com/mholt/archiver/v3"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// CompressFiles compresses all of the files matching the given paths in the
// specified directory. This function also supports passing nested paths to only
// compress certain files and folders when working in a larger directory. This
// effectively creates a local backup, but rather than ignoring specific files
// and folders, it takes an allow-list of files and folders.
//
// All paths are relative to the dir that is passed in as the first argument,
// and the compressed file will be placed at that location named
// `archive-{date}.tar.gz`.
func (fs *Filesystem) CompressFiles(dir string, paths []string) (os.FileInfo, error) {
	cleanedRootDir, err := fs.SafePath(dir)
	if err != nil {
		return nil, err
	}

	// Take all of the paths passed in and merge them together with the root directory we've gotten.
	for i, p := range paths {
		paths[i] = filepath.Join(cleanedRootDir, p)
	}

	cleaned, err := fs.ParallelSafePath(paths)
	if err != nil {
		return nil, err
	}

	a := &Archive{BasePath: cleanedRootDir, Files: cleaned}
	d := path.Join(
		cleanedRootDir,
		fmt.Sprintf("archive-%s.tar.gz", strings.ReplaceAll(time.Now().Format(time.RFC3339), ":", "")),
	)

	if err := a.Create(d); err != nil {
		return nil, err
	}

	f, err := os.Stat(d)
	if err != nil {
		_ = os.Remove(d)
		return nil, err
	}

	if err := fs.HasSpaceFor(f.Size()); err != nil {
		_ = os.Remove(d)
		return nil, err
	}

	fs.addDisk(f.Size())

	return f, nil
}

// SpaceAvailableForDecompression looks through a given archive and determines
// if decompressing it would put the server over its allocated disk space limit.
func (fs *Filesystem) SpaceAvailableForDecompression(dir string, file string) error {
	// Don't waste time trying to determine this if we know the server will have the space for
	// it since there is no limit.
	if fs.MaxDisk() <= 0 {
		return nil
	}

	source, err := fs.SafePath(filepath.Join(dir, file))
	if err != nil {
		return err
	}

	// Get the cached size in a parallel process so that if it is not cached we are not
	// waiting an unnecessary amount of time on this call.
	dirSize, err := fs.DiskUsage(false)

	var size int64
	// Walk over the archive and figure out just how large the final output would be from unarchiving it.
	err = archiver.Walk(source, func(f archiver.File) error {
		if atomic.AddInt64(&size, f.Size())+dirSize > fs.MaxDisk() {
			return newFilesystemError(ErrCodeDiskSpace, nil)
		}
		return nil
	})
	if err != nil {
		if IsUnknownArchiveFormatError(err) {
			return newFilesystemError(ErrCodeUnknownArchive, err)
		}
		return err
	}
	return err
}

// DecompressFile will decompress a file in a given directory by using the
// archiver tool to infer the file type and go from there. This will walk over
// all of the files within the given archive and ensure that there is not a
// zip-slip attack being attempted by validating that the final path is within
// the server data directory.
func (fs *Filesystem) DecompressFile(dir string, file string) error {
	source, err := fs.SafePath(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	// Ensure that the source archive actually exists on the system.
	if _, err := os.Stat(source); err != nil {
		return errors.WithStack(err)
	}

	// Walk all of the files in the archiver file and write them to the disk. If any
	// directory is encountered it will be skipped since we handle creating any missing
	// directories automatically when writing files.
	err = archiver.Walk(source, func(f archiver.File) error {
		if f.IsDir() {
			return nil
		}
		p := filepath.Join(dir, ExtractNameFromArchive(f))
		// If it is ignored, just don't do anything with the file and skip over it.
		if err := fs.IsIgnored(p); err != nil {
			return nil
		}
		if err := fs.Writefile(p, f); err != nil {
			return wrapError(err, source)
		}
		// Update the file permissions to the one set in the archive.
		if err := fs.Chmod(p, f.Mode()); err != nil {
			return wrapError(err, source)
		}
		// Update the file modification time to the one set in the archive.
		if err := fs.Chtimes(p, f.ModTime(), f.ModTime()); err != nil {
			return wrapError(err, source)
		}
		return nil
	})
	if err != nil {
		if IsUnknownArchiveFormatError(err) {
			return newFilesystemError(ErrCodeUnknownArchive, err)
		}
		return err
	}
	return nil
}

// ExtractNameFromArchive looks at an archive file to try and determine the name
// for a given element in an archive. Because of... who knows why, each file type
// uses different methods to determine the file name.
//
// If there is a archiver.File#Sys() value present we will try to use the name
// present in there, otherwise falling back to archiver.File#Name() if all else
// fails. Without this logic present, some archive types such as zip/tars/etc.
// will write all of the files to the base directory, rather than the nested
// directory that is expected.
//
// For files like ".rar" types, there is no f.Sys() value present, and the value
// of archiver.File#Name() will be what you need.
func ExtractNameFromArchive(f archiver.File) string {
	sys := f.Sys()
	// Some archive types won't have a value returned when you call f.Sys() on them,
	// such as ".rar" archives for example. In those cases the only thing you can do
	// is hope that "f.Name()" is actually correct for them.
	if sys == nil {
		return f.Name()
	}
	str := f.Name()
	switch s := sys.(type) {
	case *zip.FileHeader:
		str = s.Name
		break
	case *zip2.FileHeader:
		str = s.Name
		break
	case *tar.Header:
		str = s.Name
		break
	case *gzip.Header:
		str = s.Name
		break
	case *gzip2.Header:
		str = s.Name
		break
	default:
		str = f.Name()
		// At this point we cannot figure out what type of archive this might be so
		// just try to find the name field in the struct. If it is found return it.
		field := reflect.Indirect(reflect.ValueOf(sys)).FieldByName("Name")
		if field.IsValid() {
			return field.String()
		}
		// Fallback to the basename of the file at this point. There is nothing we can really
		// do to try and figure out what the underlying directory of the file is supposed to
		// be since it didn't implement a name field.
		return f.Name()
	}
	// 判断
	length := len(str)
    var i int = 0
	var ifgbk bool
    for i < length {
        if str[i] <= 0x7f {
            //编码0~127,只有一个字节的编码，兼容ASCII码
            i++
            ifgbk = true
        } else {
            //大于127的使用双字节编码，落在gbk编码范围内的字符
            if  str[i] >= 0x81 &&
                str[i] <= 0xfe &&
                str[i + 1] >= 0x40 &&
                str[i + 1] <= 0xfe &&
                str[i + 1] != 0xf7 {
                i += 2
                ifgbk = true
            } else {
                ifgbk = false
            }
        }
    }
	// 转换
	if ifgbk == true {
		utf8Str, _ := simplifiedchinese.GBK.NewDecoder().Bytes([]byte(str))
		return string(utf8Str)
	} else {
		return str
	}
}
