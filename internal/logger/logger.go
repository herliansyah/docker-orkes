package logger

import (
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Event represents a structured diagnostic occurrence in the Control Plane.
type Event struct {
	ID        int64          `json:"id"`
	Timestamp time.Time      `json:"timestamp"`
	Level     string         `json:"level"`
	Module    string         `json:"module"`
	Target    string         `json:"target,omitempty"`
	Message   string         `json:"message"`
	Attrs     map[string]any `json:"attrs,omitempty"`
}

// Broadcaster manages telemetry subscribers and a bounded ring buffer of recent events.
// ponytail: Uses in-memory fixed-capacity ring buffer and non-blocking channel fan-out with zero external dependencies.
// Known ceiling: drops events for slow/stalled subscribers when channel buffer (64) is full to protect control plane latency.
type Broadcaster struct {
	mu          sync.RWMutex
	subscribers map[chan Event]struct{}
	ringBuffer  []Event
	ringCap     int
	nextID      int64
}

var (
	defaultBroadcasterMu sync.RWMutex
	defaultBroadcaster   *Broadcaster
)

// NewBroadcaster creates a Broadcaster with a fixed ring buffer capacity.
func NewBroadcaster(capacity int) *Broadcaster {
	if capacity <= 0 {
		capacity = 100
	}
	return &Broadcaster{
		subscribers: make(map[chan Event]struct{}),
		ringBuffer:  make([]Event, 0, capacity),
		ringCap:     capacity,
	}
}

// DefaultBroadcaster returns the global default telemetry broadcaster.
func DefaultBroadcaster() *Broadcaster {
	defaultBroadcasterMu.RLock()
	defer defaultBroadcasterMu.RUnlock()
	if defaultBroadcaster == nil {
		defaultBroadcaster = NewBroadcaster(100)
	}
	return defaultBroadcaster
}

// SetDefaultBroadcaster sets the global default telemetry broadcaster.
func SetDefaultBroadcaster(b *Broadcaster) {
	defaultBroadcasterMu.Lock()
	defaultBroadcaster = b
	defaultBroadcasterMu.Unlock()
}

// Broadcast distributes an event to all subscribers and records it in the ring buffer.
func (b *Broadcaster) Broadcast(evt Event) {
	b.mu.Lock()
	b.nextID++
	evt.ID = b.nextID
	if len(b.ringBuffer) >= b.ringCap {
		copy(b.ringBuffer, b.ringBuffer[1:])
		b.ringBuffer[len(b.ringBuffer)-1] = evt
	} else {
		b.ringBuffer = append(b.ringBuffer, evt)
	}

	subs := make([]chan Event, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	for _, ch := range subs {
		select {
		case ch <- evt:
		default:
			// Non-blocking drop on slow subscriber to protect control plane throughput and prevent memory bloat
		}
	}
}

// Subscribe returns a receiving channel and an unsubscription cleanup function.
func (b *Broadcaster) Subscribe() (chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	ch := make(chan Event, 64)
	b.subscribers[ch] = struct{}{}
	unsub := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subscribers, ch)
	}
	return ch, unsub
}

// History returns a snapshot copy of the ring buffer.
func (b *Broadcaster) History() []Event {
	b.mu.RLock()
	defer b.mu.RUnlock()
	res := make([]Event, len(b.ringBuffer))
	copy(res, b.ringBuffer)
	return res
}

// rotatingFileWriter implements simple size-capped file rotation without third-party dependencies.
// ponytail: Rotates when file reaches maxSize (default 10MB) to file.1. Maximum total disk footprint is 2 * maxSize.
type rotatingFileWriter struct {
	mu       sync.Mutex
	path     string
	maxSize  int64
	currSize int64
	file     *os.File
}

func newRotatingFileWriter(path string, maxSize int64) (*rotatingFileWriter, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		_ = os.MkdirAll(dir, 0755)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}
	var size int64
	if fi, err := f.Stat(); err == nil {
		size = fi.Size()
	}
	if maxSize <= 0 {
		maxSize = 10 * 1024 * 1024 // 10MB
	}
	return &rotatingFileWriter{
		path:     path,
		maxSize:  maxSize,
		currSize: size,
		file:     f,
	}, nil
}

func (w *rotatingFileWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.file == nil {
		return 0, os.ErrClosed
	}

	if w.currSize+int64(len(p)) > w.maxSize {
		_ = w.file.Close()
		backupPath := w.path + ".1"
		_ = os.Remove(backupPath)
		_ = os.Rename(w.path, backupPath)
		f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if err != nil {
			return 0, err
		}
		w.file = f
		w.currSize = 0
	}

	n, err = w.file.Write(p)
	w.currSize += int64(n)
	return n, err
}

func (w *rotatingFileWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file != nil {
		err := w.file.Close()
		w.file = nil
		return err
	}
	return nil
}

type broadcastHandler struct {
	primary     slog.Handler
	broadcaster *Broadcaster
	attrs       []slog.Attr
	groups      []string
}

func (h *broadcastHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return true
}

func (h *broadcastHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.primary != nil && h.primary.Enabled(ctx, r.Level) {
		_ = h.primary.Handle(ctx, r)
	}

	if h.broadcaster == nil {
		return nil
	}

	attrs := make(map[string]any)
	module := "controlplane"
	target := ""

	for _, a := range h.attrs {
		attrs[a.Key] = a.Value.Any()
		if a.Key == "module" {
			module = a.Value.String()
		}
		if a.Key == "target" || a.Key == "node" {
			target = a.Value.String()
		}
	}

	r.Attrs(func(a slog.Attr) bool {
		attrs[a.Key] = a.Value.Any()
		if a.Key == "module" {
			module = a.Value.String()
		}
		if a.Key == "target" || a.Key == "node" {
			target = a.Value.String()
		}
		return true
	})

	lvlStr := r.Level.String()
	evt := Event{
		Timestamp: r.Time,
		Level:     lvlStr,
		Module:    module,
		Target:    target,
		Message:   r.Message,
		Attrs:     attrs,
	}

	h.broadcaster.Broadcast(evt)
	return nil
}

func (h *broadcastHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	var nextPrimary slog.Handler
	if h.primary != nil {
		nextPrimary = h.primary.WithAttrs(attrs)
	}
	return &broadcastHandler{
		primary:     nextPrimary,
		broadcaster: h.broadcaster,
		attrs:       append(append([]slog.Attr(nil), h.attrs...), attrs...),
		groups:      h.groups,
	}
}

func (h *broadcastHandler) WithGroup(name string) slog.Handler {
	var nextPrimary slog.Handler
	if h.primary != nil {
		nextPrimary = h.primary.WithGroup(name)
	}
	return &broadcastHandler{
		primary:     nextPrimary,
		broadcaster: h.broadcaster,
		attrs:       h.attrs,
		groups:      append(append([]string(nil), h.groups...), name),
	}
}

// Config configures the Diagnostic Logger.
type Config struct {
	Level    string // "debug", "info", "warn", "error"
	FilePath string // optional file path for log sink (rotates at 10MB)
	Silent   bool   // suppress stderr output
}

// Result holds the configured logger and any file closer.
type Result struct {
	Logger      *slog.Logger
	Closer      io.Closer
	Broadcaster *Broadcaster
}

// LevelFromString parses string into slog.Level.
func LevelFromString(s string) (slog.Level, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, true
	case "info", "":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	default:
		return slog.LevelInfo, false
	}
}

// Setup initializes slog and hooks log.Default() via a multiwriter bridge.
func Setup(cfg Config) (*Result, error) {
	lvl, _ := LevelFromString(cfg.Level)

	var fileWriter io.WriteCloser
	var writers []io.Writer

	if cfg.FilePath != "" {
		rfw, err := newRotatingFileWriter(cfg.FilePath, 10*1024*1024)
		if err != nil {
			return nil, err
		}
		fileWriter = rfw
		writers = append(writers, rfw)
	}

	if !cfg.Silent {
		writers = append(writers, os.Stderr)
	}

	var sink io.Writer
	if len(writers) == 0 {
		sink = io.Discard
	} else if len(writers) == 1 {
		sink = writers[0]
	} else {
		sink = io.MultiWriter(writers...)
	}

	opts := &slog.HandlerOptions{
		Level: lvl,
	}

	handler := slog.NewTextHandler(sink, opts)
	broadcaster := NewBroadcaster(100)
	SetDefaultBroadcaster(broadcaster)

	bHandler := &broadcastHandler{
		primary:     handler,
		broadcaster: broadcaster,
	}

	l := slog.New(bHandler)
	slog.SetDefault(l)

	// ponytail: Bridge adapter connecting legacy log.Printf to broadcaster and sink.
	// Parses prefix [WARN], [ERROR], [DEBUG] if present.
	bridge := &legacyLogBridge{
		sink:        sink,
		broadcaster: broadcaster,
	}
	log.SetOutput(bridge)
	log.SetFlags(log.Ldate | log.Ltime)

	return &Result{
		Logger:      l,
		Closer:      fileWriter,
		Broadcaster: broadcaster,
	}, nil
}

type legacyLogBridge struct {
	sink        io.Writer
	broadcaster *Broadcaster
}

func (b *legacyLogBridge) Write(p []byte) (n int, err error) {
	n, err = b.sink.Write(p)
	if b.broadcaster == nil {
		return n, err
	}

	line := strings.TrimSpace(string(p))
	if line == "" {
		return n, err
	}

	level := "INFO"
	module := "controlplane"
	msg := line

	// Strip timestamp prefix added by log.Ldate | log.Ltime if present (e.g. 2026/10/14 08:30:00 [WARN] ...)
	parts := strings.SplitN(line, " ", 3)
	if len(parts) >= 3 && len(parts[0]) == 10 && len(parts[1]) == 8 {
		msg = parts[2]
	}

	if strings.HasPrefix(msg, "[WARN]") {
		level = "WARN"
		msg = strings.TrimSpace(strings.TrimPrefix(msg, "[WARN]"))
	} else if strings.HasPrefix(msg, "[ERROR]") {
		level = "ERROR"
		msg = strings.TrimSpace(strings.TrimPrefix(msg, "[ERROR]"))
	} else if strings.HasPrefix(msg, "[DEBUG]") {
		level = "DEBUG"
		msg = strings.TrimSpace(strings.TrimPrefix(msg, "[DEBUG]"))
	}

	b.broadcaster.Broadcast(Event{
		Timestamp: time.Now(),
		Level:     level,
		Module:    module,
		Message:   msg,
	})

	return n, err
}

// Helpers for structured emission
func Emit(level slog.Level, module, target, msg string, attrs map[string]any) {
	var slogAttrs []any
	if module != "" {
		slogAttrs = append(slogAttrs, slog.String("module", module))
	}
	if target != "" {
		slogAttrs = append(slogAttrs, slog.String("target", target))
	}
	for k, v := range attrs {
		slogAttrs = append(slogAttrs, slog.Any(k, v))
	}
	slog.Default().Log(context.Background(), level, msg, slogAttrs...)
}

func Info(module, target, msg string, attrs ...any) {
	emitHelper(slog.LevelInfo, module, target, msg, attrs...)
}

func Warn(module, target, msg string, attrs ...any) {
	emitHelper(slog.LevelWarn, module, target, msg, attrs...)
}

func Error(module, target, msg string, attrs ...any) {
	emitHelper(slog.LevelError, module, target, msg, attrs...)
}

func Debug(module, target, msg string, attrs ...any) {
	emitHelper(slog.LevelDebug, module, target, msg, attrs...)
}

func emitHelper(level slog.Level, module, target, msg string, attrs ...any) {
	attrMap := make(map[string]any)
	for i := 0; i < len(attrs)-1; i += 2 {
		key, ok := attrs[i].(string)
		if ok {
			attrMap[key] = attrs[i+1]
		}
	}
	Emit(level, module, target, msg, attrMap)
}
