package app

import (
	"os"
	"path/filepath"
	"sync"

	"gopkg.in/natefinch/lumberjack.v2"

	"github.com/Mag1cFall/AIStudio2API/internal/config"
)

// logFileName is the current log file name in the log directory.
const logFileName = "aistudio2api.log"

// logOutput writes console logs to stderr and an optional rotating log file.
type logOutput struct {
	dir  string
	mu   sync.Mutex
	file *lumberjack.Logger
}

// set enables or disables file logging; rotates single files at config.LogFileMB, deleting oldest files when directory exceeds maxTotalMB.
func (output *logOutput) set(enabled bool, maxTotalMB int) error {
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.file != nil {
		if err := output.file.Close(); err != nil {
			return err
		}
		output.file = nil
	}
	if !enabled {
		return nil
	}
	output.file = &lumberjack.Logger{
		Filename:   filepath.Join(output.dir, logFileName),
		MaxSize:    config.LogFileMB,
		MaxBackups: maxTotalMB/config.LogFileMB - 1,
		LocalTime:  true,
	}
	return nil
}

// Write writes a log entry to stderr and log file.
func (output *logOutput) Write(data []byte) (int, error) {
	os.Stderr.Write(data)
	output.mu.Lock()
	defer output.mu.Unlock()
	if output.file != nil {
		return output.file.Write(data)
	}
	return len(data), nil
}
