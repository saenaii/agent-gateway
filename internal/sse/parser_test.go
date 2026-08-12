package sse

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestParserEvents(t *testing.T) {
	stream := "data: {\"a\":1}\n\n" +
		"event: usage\n" +
		"data: {\"tokens\":42}\n\n" +
		"data: [DONE]\n\n"
	p := NewParser(strings.NewReader(stream))

	ev, err := p.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != `{"a":1}` || ev.Type != "message" {
		t.Errorf("event = %+v", ev)
	}

	ev, err = p.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != `{"tokens":42}` || ev.Type != "usage" {
		t.Errorf("event = %+v", ev)
	}

	_, err = p.Next()
	if !errors.Is(err, ErrDone) {
		t.Errorf("err = %v, want ErrDone", err)
	}
}

func TestParserClosedWithoutDone(t *testing.T) {
	p := NewParser(strings.NewReader("data: x\n\n"))
	ev, err := p.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != "x" {
		t.Errorf("data = %q", ev.Data)
	}
	if _, err := p.Next(); !errors.Is(err, ErrClosed) {
		t.Errorf("err = %v, want ErrClosed", err)
	}
}

func TestParserEmptyStream(t *testing.T) {
	p := NewParser(strings.NewReader(""))
	if _, err := p.Next(); !errors.Is(err, ErrClosed) && !errors.Is(err, io.EOF) {
		t.Errorf("err = %v", err)
	}
}

func TestParserMultilineData(t *testing.T) {
	p := NewParser(strings.NewReader("data: line1\ndata: line2\n\n"))
	ev, err := p.Next()
	if err != nil {
		t.Fatal(err)
	}
	if ev.Data != "line1\nline2" {
		t.Errorf("data = %q", ev.Data)
	}
}
