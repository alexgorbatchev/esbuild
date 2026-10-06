package fs

import "os"

func (fp goFilepath) lstat(path string) (os.FileInfo, error) {
	if fp.input != nil {
		return fp.input.Lstat(path)
	}
	return os.Lstat(path)
}

func (fp goFilepath) readlink(path string) (string, error) {
	if fp.input != nil {
		return fp.input.Readlink(path)
	}
	return os.Readlink(path)
}
