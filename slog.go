//go:build go1.21

package zerolog

import (
	"context"
	"encoding"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"runtime"
	"time"
)

// SlogHandlerOptions are options for a SlogHandler.
// A zero SlogHandlerOptions consists entirely of default values.
type SlogHandlerOptions struct {
	// AddSource causes the handler to compute the source code position
	// of the log statement and add a SourceKey attribute to the output.
	AddSource bool

	// Level reports the minimum record level that will be logged.
	// The handler discards records with lower levels.
	// If Level is nil, the handler assumes the level set in the logger.
	// The handler calls Level.Level if it's not nil for each record processed;
	// to adjust the minimum level dynamically, use a LevelVar.
	Level slog.Leveler
}

// SlogHandler is an slog.Handler implementation that uses zerolog to process slog.Record.
type SlogHandler struct {
	opts   *SlogHandlerOptions
	logger Logger
}

// NewSlogHandler creates a *ZerologHandler implementing slog.Handler.
// It wraps a Logger to which log records will be sent.
//
// Unlesse opts.Level is not nil, the logger level is used to filter out records, otherwise
// opts.Level is used.
//
// The provided logger instance must be configured to not send timestamps or caller information.
//
// If opts is nil, it assumes default options values.
//
// # Caution:
//
// The provided logger must not be configured to write the timestamp or the caller as those fields are provided by slog records.
func NewSlogHandler(logger Logger, opts *SlogHandlerOptions) *SlogHandler {
	if opts == nil {
		opts = new(SlogHandlerOptions)
	}
	logger.With().Timestamp()
	opt := *opts // Copy
	return &SlogHandler{
		opts:   &opt,
		logger: logger,
	}
}

// Enabled implements slog.Handler.
func (h *SlogHandler) Enabled(_ context.Context, lvl slog.Level) bool {
	if h.opts.Level != nil {
		return lvl >= h.opts.Level.Level()
	}
	return zerologLevel(lvl) >= h.logger.GetLevel()
}

// Handle implements slog.Handler.
func (h *SlogHandler) Handle(_ context.Context, rec slog.Record) error {
	logger := h.logger
	if h.opts.Level != nil {
		logger = h.logger.Level(zerologLevel(h.opts.Level.Level()))
	}
	evt := logger.WithLevel(zerologLevel(rec.Level))

	rec.Attrs(func(a slog.Attr) bool {
		mapAttr(evt, a)
		return true
	})

	if h.opts.AddSource && rec.PC > 0 {
		frame, _ := runtime.CallersFrames([]uintptr{rec.PC}).Next()
		evt.Str(CallerFieldName, fmt.Sprintf("%s:%d", frame.File, frame.Line))
	}
	evt = evt.closeGroup(-1)
	evt.Time(TimestampFieldName, rec.Time)
	evt.Msg(rec.Message)
	return nil
}

// WithAttrs implements slog.Handler.
func (h *SlogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SlogHandler{
		opts:   h.opts,
		logger: mapAttrs(h.logger.With(), attrs...).Logger(),
	}
}

// WithGroup implements slog.Handler.
func (h *SlogHandler) WithGroup(name string) slog.Handler {
	return &SlogHandler{
		opts:   h.opts,
		logger: h.logger.With().openGroup(name).Logger(),
	}
}

// zlogWriter is an interface with methods common between
// Context and *Event. This interface is
// implemented by both Context and *Event.
type zlogWriter[E any] interface {
	Bool(string, bool) E
	Dur(string, time.Duration) E
	Float64(string, float64) E
	Int64(string, int64) E
	Str(string, string) E
	Time(string, time.Time) E
	Uint64(string, uint64) E
	// Dict(string, *Event) E
	Interface(string, any) E
	AnErr(string, error) E
	Stringer(string, fmt.Stringer) E
	IPAddr(string, net.IP) E
	IPPrefix(string, net.IPNet) E
	MACAddr(string, net.HardwareAddr) E
	RawJSON(string, []byte) E
	grouped(string, func(E) E) E
}

var (
	_ zlogWriter[*Event]  = (*Event)(nil)
	_ zlogWriter[Context] = Context{}
)

// mapAttrs writes multiple slog.Attr into the target which is either a Context
// or a *Event.
func mapAttrs[T zlogWriter[T]](target T, a ...slog.Attr) T {
	for _, attr := range a {
		target = mapAttr(target, attr)
	}
	return target
}

// mapAttr writes slog.Attr into the target which is either a Context
// or a *Event.
func mapAttr[T zlogWriter[T]](target T, a slog.Attr) T {
	value := a.Value.Resolve()
	switch value.Kind() {
	case slog.KindGroup:
		// return target.Dict(a.Key, mapAttrs(Dict(), value.Group()...))
		return target.grouped(a.Key, func(t T) T {
			return mapAttrs(t, value.Group()...)
		})
	case slog.KindBool:
		return target.Bool(a.Key, value.Bool())
	case slog.KindDuration:
		return target.Dur(a.Key, value.Duration())
	case slog.KindFloat64:
		return target.Float64(a.Key, value.Float64())
	case slog.KindInt64:
		return target.Int64(a.Key, value.Int64())
	case slog.KindString:
		return target.Str(a.Key, value.String())
	case slog.KindTime:
		return target.Time(a.Key, value.Time())
	case slog.KindUint64:
		return target.Uint64(a.Key, value.Uint64())
	case slog.KindAny:
		fallthrough
	default:
		return mapAttrAny(target, a.Key, value.Any())
	}
}

func mapAttrAny[T zlogWriter[T]](target T, key string, value any) T {
	switch v := value.(type) {
	case net.IP:
		return target.IPAddr(key, v)
	case net.IPNet:
		return target.IPPrefix(key, v)
	case net.HardwareAddr:
		return target.MACAddr(key, v)
	case error:
		return target.AnErr(key, v)
	case fmt.Stringer:
		return target.Stringer(key, v)
	case json.Marshaler:
		txt, err := v.MarshalJSON()
		if err == nil {
			return target.RawJSON(key, txt)
		}
		return target.Str(key, "!ERROR:"+err.Error())
	case encoding.TextMarshaler:
		txt, err := v.MarshalText()
		if err == nil {
			return target.Str(key, string(txt))
		}
		return target.Str(key, "!ERROR:"+err.Error())
	default:
		return target.Interface(key, value)
	}
}

// zerologLevel maps slog.Level into Level.
func zerologLevel(lvl slog.Level) Level {
	switch {
	case lvl < slog.LevelDebug:
		return TraceLevel
	case lvl < slog.LevelInfo:
		return DebugLevel
	case lvl < slog.LevelWarn:
		return InfoLevel
	case lvl < slog.LevelError:
		return WarnLevel
	default:
		return ErrorLevel
	}
}
