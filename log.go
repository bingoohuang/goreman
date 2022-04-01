package main

import (
	"bytes"
	"fmt"
	"time"

	"github.com/mattn/go-colorable"
)

type clogger struct {
	colorIndex int
	name       string
	writes     chan []byte
	done       chan struct{}
	timeout    time.Duration // how long to wait before printing partial lines
	buffers    bytes.Buffer  // partial lines awaiting printing
}

var colors = []int{
	32, // green
	36, // cyan
	35, // magenta
	33, // yellow
	34, // blue
	31, // red
}

var out = colorable.NewColorableStdout()

// write any stored buffers, plus the given line, then empty out
// the buffers.
func (l *clogger) writeBuffers(line []byte) {
	fmt.Fprintf(out, "\x1b[%dm", colors[l.colorIndex])
	if *logTime {
		now := time.Now().Format("2006-01-02 15:04:05")
		fmt.Fprintf(out, "%s %*s | ", now, maxProcNameLength, l.name)
	} else {
		fmt.Fprintf(out, "%*s | ", maxProcNameLength, l.name)
	}
	fmt.Fprintf(out, "\x1b[m")
	l.buffers.Write(line)
	l.buffers.WriteTo(out)
	l.buffers.Reset()
}

// bundle writes into lines, waiting briefly for completion of lines
func (l *clogger) writeLines() {
	var tick <-chan time.Time
	for {
		select {
		case w, ok := <-l.writes:
			if !ok {
				if l.buffers.Len() > 0 {
					l.writeBuffers([]byte("\n"))
				}
				return
			}
			buf := bytes.NewBuffer(w)
			for {
				line, err := buf.ReadBytes('\n')
				if len(line) > 0 {
					if line[len(line)-1] == '\n' {
						// any text followed by a newline should flush
						// existing buffers. a bare newline should flush
						// existing buffers, but only if there are any.
						if len(line) != 1 || l.buffers.Len() > 0 {
							l.writeBuffers(line)
						}
						tick = nil
					} else {
						l.buffers.Write(line)
						tick = time.After(l.timeout)
					}
				}
				if err != nil {
					break
				}
			}
			l.done <- struct{}{}
		case <-tick:
			if l.buffers.Len() > 0 {
				l.writeBuffers([]byte("\n"))
			}
			tick = nil
		}
	}
}

// write handler of logger.
func (l *clogger) Write(p []byte) (int, error) {
	l.writes <- p
	<-l.done
	return len(p), nil
}

// create logger instance.
func createLogger(name string, colorIndex int) *clogger {
	l := &clogger{
		colorIndex: colorIndex,
		name:       name,
		writes:     make(chan []byte),
		done:       make(chan struct{}),
		timeout:    2 * time.Millisecond,
	}
	go l.writeLines()
	return l
}
