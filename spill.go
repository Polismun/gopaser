package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// jsonlSpiller writes JSON objects to a temp file (one per line) to avoid
// accumulating large slices in memory during parsing. After parsing is done
// and the demoinfocs library state is freed, the spiller can stream its
// contents as a JSON array to the final output.
type jsonlSpiller struct {
	file  *os.File
	w     *bufio.Writer
	enc   *json.Encoder
	count int
}

func newJsonlSpiller(prefix string) (*jsonlSpiller, error) {
	f, err := os.CreateTemp("", prefix+"-*.jsonl")
	if err != nil {
		return nil, fmt.Errorf("spill: create temp: %w", err)
	}
	bw := bufio.NewWriterSize(f, 64*1024) // 64 KB buffer
	enc := json.NewEncoder(bw)
	enc.SetEscapeHTML(false)
	return &jsonlSpiller{file: f, w: bw, enc: enc}, nil
}

// Append writes one JSON object as a line in the temp file.
func (s *jsonlSpiller) Append(v any) error {
	s.count++
	return s.enc.Encode(v) // Encode appends \n
}

// Count returns the number of items written.
func (s *jsonlSpiller) Count() int {
	return s.count
}

// Snapshot returns the current file position and count for later truncation.
func (s *jsonlSpiller) Snapshot() (int64, int) {
	s.w.Flush()
	pos, _ := s.file.Seek(0, io.SeekCurrent)
	return pos, s.count
}

// Truncate rewinds the spiller to a previous snapshot (used by phantom purge).
func (s *jsonlSpiller) Truncate(pos int64, count int) {
	s.w.Flush()
	s.file.Truncate(pos)
	s.file.Seek(pos, io.SeekStart)
	s.w.Reset(s.file)
	s.count = count
}

// StreamJSON reads all lines from the temp file and writes them as a JSON
// array to w: [line1,line2,...]. Each line is already valid JSON from Encode.
func (s *jsonlSpiller) StreamJSON(w io.Writer) error {
	s.w.Flush()

	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return err
	}

	if _, err := w.Write([]byte("[")); err != nil {
		return err
	}

	scanner := bufio.NewScanner(s.file)
	scanner.Buffer(make([]byte, 256*1024), 1024*1024) // up to 1 MB lines
	first := true
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if !first {
			if _, err := w.Write([]byte(",")); err != nil {
				return err
			}
		}
		first = false
		// Trim trailing newline if present (Encode adds \n)
		if line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
		}
		if _, err := w.Write(line); err != nil {
			return err
		}
	}

	if _, err := w.Write([]byte("]")); err != nil {
		return err
	}
	return scanner.Err()
}

// Close removes the temp file.
func (s *jsonlSpiller) Close() {
	if s.file != nil {
		name := s.file.Name()
		s.file.Close()
		os.Remove(name)
	}
}
