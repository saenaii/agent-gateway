// Package sse implements a minimal Server-Sent Events parser that extracts
// data events without blocking the stream, ready for Layer 2 streaming proxy.
package sse

import (
	"bufio"
	"errors"
	"io"
	"strings"
)

// Event is one parsed SSE event.
type Event struct {
	Data string
	Type string
}

// ErrDone signals the stream terminator "data: [DONE]".
var ErrDone = errors.New("sse stream finished")

// ErrClosed is returned when the underlying stream ends without a done marker.
var ErrClosed = errors.New("sse stream closed")

// Parser reads an SSE stream line by line and emits complete events.
type Parser struct {
	r *bufio.Reader
}

// NewParser wraps r in a Parser.
func NewParser(r io.Reader) *Parser {
	return &Parser{r: bufio.NewReader(r)}
}

// Next returns the next data event. It returns ErrDone when the upstream sent
// the [DONE] terminator and ErrClosed when the stream simply ends.
func (p *Parser) Next() (Event, error) {
	var data []string
	eventType := "message"
	for {
		line, err := p.readLine()
		if err != nil {
			if len(data) > 0 {
				return Event{Data: strings.Join(data, "\n"), Type: eventType}, nil
			}
			return Event{}, err
		}
		if line == "" {
			if len(data) == 0 {
				continue
			}
			ev := Event{Data: strings.Join(data, "\n"), Type: eventType}
			if ev.Data == "[DONE]" {
				return Event{}, ErrDone
			}
			return ev, nil
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch key {
		case "event":
			eventType = strings.TrimSpace(value)
		case "data":
			data = append(data, strings.TrimPrefix(value, " "))
		}
	}
}

func (p *Parser) readLine() (string, error) {
	line, err := p.r.ReadString('\n')
	if err != nil && line == "" {
		if errors.Is(err, io.EOF) {
			return "", ErrClosed
		}
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
