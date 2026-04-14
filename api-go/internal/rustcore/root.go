package rustcore

import (
	"path/filepath"
	"runtime"
)

func rustCoreDir() string {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		return "../rust-core"
	}
	return filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", "..", "rust-core"))
}
