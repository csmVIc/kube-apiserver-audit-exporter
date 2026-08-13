package exporter

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	auditv1 "k8s.io/apiserver/pkg/apis/audit/v1"
)

type Option func(e *Exporter)

func WithFile(file string) Option {
	return func(e *Exporter) {
		e.file = file
	}
}

func WithReplay(replay bool) Option {
	return func(e *Exporter) {
		e.replay = replay
	}
}

func WithStartAtEnd(startAtEnd bool) Option {
	return func(e *Exporter) {
		e.startAtEnd = startAtEnd
	}
}

func WithClusterLabel(c string) Option {
	return func(e *Exporter) {
		e.clusterLabel = c
	}
}

func NewExporter(opts ...Option) *Exporter {
	e := &Exporter{
		podCreationTimes:         map[target]*time.Time{},
		batchJobCreationTimes:    map[target]*time.Time{},
		yunikornWorkloadPodState: map[target]yunikornWorkloadPodState{},
	}

	for _, opt := range opts {
		opt(e)
	}

	return e
}

type Exporter struct {
	file       string
	openFile   *os.File
	offset     int64
	startAtEnd bool

	clusterLabel string
	replay       bool
	timeDiff     time.Duration

	podCreationTimes      map[target]*time.Time
	batchJobCreationTimes map[target]*time.Time

	yunikornWorkloadPodState map[target]yunikornWorkloadPodState
}

func ListenAndServe(addr string) error {
	mux := http.NewServeMux()
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{
		EnableOpenMetrics: true,
	})
	mux.Handle("/metrics", handler)

	slog.Info("Service started", "address", addr)
	return http.ListenAndServe(addr, mux)
}

// Run handles audit log file changes
func (p *Exporter) Run() {
	if err := p.openAuditFile(p.file, p.startAtEnd); err != nil {
		slog.Error("Error opening file", "cluster", p.clusterLabel, "error", err)
	}
	defer p.closeAuditFile()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for range ticker.C {
		p.handleFileEvent(p.file)
		ticker.Reset(100 * time.Millisecond)
	}
}

// handleFileEvent processes filesystem events
func (p *Exporter) handleFileEvent(path string) {
	if err := p.processFileUpdate(path); err != nil {
		slog.Error("Error processing file", "cluster", p.clusterLabel, "error", err)
	}
}

// processFileUpdate reads new log entries
func (p *Exporter) processFileUpdate(path string) error {
	if p.openFile == nil {
		if err := p.openAuditFile(path, p.startAtEnd); err != nil {
			return err
		}
	}

	pathInfo, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	openFileInfo, err := p.openFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat open file: %w", err)
	}

	if !os.SameFile(openFileInfo, pathInfo) {
		if err := p.readOpenFile(); err != nil {
			return fmt.Errorf("failed to finish rotated file: %w", err)
		}
		if err := p.openAuditFile(path, false); err != nil {
			return err
		}
	}

	return p.readOpenFile()
}

func (p *Exporter) openAuditFile(path string, startAtEnd bool) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}

	if err := p.closeAuditFile(); err != nil {
		file.Close()
		return err
	}

	p.openFile = file
	p.offset = 0
	if startAtEnd {
		fileInfo, err := file.Stat()
		if err != nil {
			p.closeAuditFile()
			return fmt.Errorf("failed to stat file: %w", err)
		}
		p.offset = fileInfo.Size()
	}
	return nil
}

func (p *Exporter) closeAuditFile() error {
	if p.openFile == nil {
		return nil
	}
	err := p.openFile.Close()
	p.openFile = nil
	if err != nil {
		return fmt.Errorf("failed to close file: %w", err)
	}
	return nil
}

func (p *Exporter) readOpenFile() error {
	fileInfo, err := p.openFile.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat open file: %w", err)
	}
	if fileInfo.Size() == p.offset {
		slog.Debug("No new updates in log file", "cluster", p.clusterLabel, "offset", p.offset)
		return nil
	}

	if _, err = p.openFile.Seek(p.offset, io.SeekStart); err != nil {
		return fmt.Errorf("seek failed: %w", err)
	}

	start := time.Now()
	defer func() {
		slog.Debug("File processing complete", "cluster", p.clusterLabel, "new_offset", p.offset, "duration", time.Since(start))
	}()

	reader := bufio.NewReaderSize(p.openFile, 1<<20) // 1MB buffer
	for {
		err := p.skipNull(reader)
		if err != err {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("skip error: %w", err)
		}

		line, err := reader.ReadSlice('\n')
		if err != nil {
			if err == io.EOF || err == io.ErrUnexpectedEOF {
				return nil
			}
			return fmt.Errorf("read error: %w", err)
		}

		// This means that we have mislocated the read and can no longer continue execution
		if !bytes.HasSuffix(line, []byte{'}', '\n'}) {
			return fmt.Errorf("malformed log entry: %q", line)
		}

		if !bytes.HasPrefix(line, []byte{'{'}) {
			p.offset += int64(len(line))
			continue
		}

		var event auditv1.Event
		if err := json.Unmarshal(line, &event); err != nil {
			return fmt.Errorf("json decode error: %w", err)
		}

		if p.replay {
			if p.timeDiff == 0 {
				p.timeDiff = time.Since(event.StageTimestamp.Time)
			} else {
				// Simulation has been collected to EOF
				if time.Since(event.StageTimestamp.Time) < p.timeDiff {
					return nil
				}
			}
		}

		p.updateMetrics(p.clusterLabel, event)
		p.offset += int64(len(line))
	}
}

func (p *Exporter) skipNull(reader *bufio.Reader) error {
	for {
		peek, err := reader.Peek(1)
		if err != nil {
			return err
		}
		if peek[0] != 0 {
			return nil
		}
		_, err = reader.ReadByte()
		if err != nil {
			return err
		}
		p.offset++
	}
}
