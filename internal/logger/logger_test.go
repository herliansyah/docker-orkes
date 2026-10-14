package logger

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRingBufferBoundedMemory(t *testing.T) {
	b := NewBroadcaster(10)
	for i := 1; i <= 25; i++ {
		b.Broadcast(Event{
			Timestamp: time.Now(),
			Level:     "INFO",
			Module:    "deploy",
			Message:   fmt.Sprintf("event-%d", i),
		})
	}

	history := b.History()
	if len(history) != 10 {
		t.Fatalf("expected ring buffer size 10, got %d", len(history))
	}

	// Oldest remaining should be event-16, newest should be event-25
	if history[0].Message != "event-16" {
		t.Errorf("expected oldest event to be event-16, got %s", history[0].Message)
	}
	if history[9].Message != "event-25" {
		t.Errorf("expected newest event to be event-25, got %s", history[9].Message)
	}
}

func TestBroadcasterNonBlockingDrop(t *testing.T) {
	b := NewBroadcaster(10)
	ch, unsub := b.Subscribe()
	defer unsub()

	// Subscriber has channel buffer 64. Broadcast 100 events without reading.
	// Must not block or deadlock.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b.Broadcast(Event{
				Timestamp: time.Now(),
				Level:     "DEBUG",
				Module:    "poller",
				Message:   "poll",
			})
		}
		close(done)
	}()

	select {
	case <-done:
		// Succeeded without hanging
	case <-time.After(2 * time.Second):
		t.Fatal("broadcaster blocked on slow subscriber")
	}

	// Read buffered items
	received := 0
	for len(ch) > 0 {
		<-ch
		received++
	}
	if received != 64 {
		t.Errorf("expected exactly 64 buffered events, got %d", received)
	}
}

func TestLegacyLogBridge(t *testing.T) {
	var buf bytes.Buffer
	b := NewBroadcaster(5)
	bridge := &legacyLogBridge{
		sink:        &buf,
		broadcaster: b,
	}

	log.SetOutput(bridge)
	log.SetFlags(0) // no prefix for simple check

	log.Printf("[WARN] remote node unreachable")

	history := b.History()
	if len(history) != 1 {
		t.Fatalf("expected 1 event in broadcaster, got %d", len(history))
	}

	evt := history[0]
	if evt.Level != "WARN" {
		t.Errorf("expected Level WARN, got %s", evt.Level)
	}
	if evt.Message != "remote node unreachable" {
		t.Errorf("expected cleaned message, got %s", evt.Message)
	}
}

func TestRotatingFileWriter(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "orkes-log-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	logPath := filepath.Join(tmpDir, "test.log")
	// Cap at 200 bytes
	rfw, err := newRotatingFileWriter(logPath, 200)
	if err != nil {
		t.Fatal(err)
	}
	defer rfw.Close()

	chunk := bytes.Repeat([]byte("A"), 150)
	_, _ = rfw.Write(chunk)
	// Second write exceeds 200 bytes, triggers rotation to test.log.1
	_, _ = rfw.Write(chunk)

	if _, err := os.Stat(logPath + ".1"); os.IsNotExist(err) {
		t.Errorf("expected rotated backup file test.log.1 to exist")
	}
}

func TestSetupAndEmit(t *testing.T) {
	res, err := Setup(Config{
		Level:  "debug",
		Silent: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Closer != nil {
		defer res.Closer.Close()
	}

	Info("deploy", "node-1", "deployment started", "version", "v1.2.0")

	history := res.Broadcaster.History()
	if len(history) == 0 {
		t.Fatal("expected broadcasted event in history")
	}
	last := history[len(history)-1]
	if last.Module != "deploy" || last.Target != "node-1" {
		t.Errorf("unexpected event: %+v", last)
	}
	if last.Attrs["version"] != "v1.2.0" {
		t.Errorf("expected version attr: %+v", last.Attrs)
	}
}
