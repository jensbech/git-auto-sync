package common

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
)

const (
	LogMaxBytes = 5 * 1024 * 1024
	LogMaxFiles = 3
)

var debugEnabled = os.Getenv("GAS_DEBUG") == "1"

func DebugEnabled() bool {
	return debugEnabled
}

func Debugf(format string, args ...interface{}) {
	if debugEnabled {
		log.Printf(format, args...)
	}
}

type RotatingWriter struct {
	mu       sync.Mutex
	path     string
	maxBytes int64
	maxFiles int
	f        *os.File
	size     int64
}

func NewRotatingWriter(path string, maxBytes int64, maxFiles int) (*RotatingWriter, error) {
	if maxFiles < 1 {
		maxFiles = 1
	}
	w := &RotatingWriter{path: path, maxBytes: maxBytes, maxFiles: maxFiles}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *RotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return err
	}
	w.f = f
	w.size = info.Size()
	return nil
}

func (w *RotatingWriter) rotate() error {
	if w.f != nil {
		_ = w.f.Close()
		w.f = nil
	}
	if w.maxFiles == 1 {
		if err := os.Truncate(w.path, 0); err != nil && !os.IsNotExist(err) {
			return err
		}
		return w.open()
	}
	_ = os.Remove(w.backupName(w.maxFiles - 1))
	for i := w.maxFiles - 2; i >= 1; i-- {
		_ = os.Rename(w.backupName(i), w.backupName(i+1))
	}
	if err := os.Rename(w.path, w.backupName(1)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return w.open()
}

func (w *RotatingWriter) backupName(i int) string {
	return fmt.Sprintf("%s.%d", w.path, i)
}

func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.f == nil {
		if err := w.open(); err != nil {
			return 0, err
		}
	}
	if w.maxBytes > 0 && w.size > 0 && w.size+int64(len(p)) > w.maxBytes {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}
