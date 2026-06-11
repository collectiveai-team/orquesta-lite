package web

import (
	"os"
	"strings"
)

// logTail incrementally reads complete lines appended to a file. It tolerates
// the file not existing yet, being rotated (size shrinks -> reread from the
// start), and partial writes (incomplete last lines are buffered until the
// newline arrives).
type logTail struct {
	path    string
	offset  int64
	partial string
	primed  bool
	replay  int // number of trailing lines to emit on the first read
}

func newLogTail(path string, replay int) *logTail {
	return &logTail{path: path, replay: replay}
}

// Next returns any new complete lines appended since the previous call. On
// the first call it returns only the last `replay` lines of the existing file.
func (t *logTail) Next() []string {
	info, err := os.Stat(t.path)
	if err != nil {
		return nil
	}
	if info.Size() < t.offset {
		// Rotated/truncated: start over.
		t.offset = 0
		t.partial = ""
	}

	if !t.primed {
		t.primed = true
		lines := t.readAll()
		if len(lines) > t.replay {
			lines = lines[len(lines)-t.replay:]
		}
		return lines
	}
	return t.readAll()
}

func (t *logTail) readAll() []string {
	f, err := os.Open(t.path)
	if err != nil {
		return nil
	}
	defer f.Close()

	if _, err := f.Seek(t.offset, 0); err != nil {
		return nil
	}
	buf := make([]byte, 256*1024)
	var chunk strings.Builder
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk.Write(buf[:n])
			t.offset += int64(n)
		}
		if err != nil {
			break
		}
	}

	data := t.partial + chunk.String()
	if data == "" {
		return nil
	}
	parts := strings.Split(data, "\n")
	t.partial = parts[len(parts)-1] // "" when data ended with \n
	parts = parts[:len(parts)-1]

	lines := parts[:0]
	for _, l := range parts {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}
