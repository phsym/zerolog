//go:build go1.21 && !binary_log

package zerolog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"maps"

	"testing/slogtest"
)

type stringer struct{}

func (stringer) String() string {
	return "stringer"
}

type marshaller struct{ err error }

func (m marshaller) MarshalText() (text []byte, err error) {
	return []byte("marshaller"), m.err
}

type jsoner struct {
	foo string
	err error
}

func (j jsoner) MarshalJSON() ([]byte, error) {
	return []byte(fmt.Sprintf(`{"foo": %q}`, j.foo)), j.err
}

type unknown struct{ Foo string }

var (
	now = time.Now()

	attrs = []slog.Attr{
		slog.String("titi", "toto"),
		slog.String("tata", "tutu"),
		slog.Int("foo", 12),
		slog.Uint64("bar", 42),
		slog.Duration("dur", 3*time.Second),
		slog.Bool("bool", true),
		slog.Float64("float", 23.7),
		slog.Time("thetime", now),
		slog.Any("err", errors.New("yo")),
		slog.Group("empty"),
		slog.Group("group", slog.String("bar", "baz")),
		slog.Any("ip", net.IP{192, 168, 1, 2}),
		slog.Any("ipnet", net.IPNet{IP: net.IP{192, 168, 1, 0}, Mask: net.IPv4Mask(255, 255, 255, 0)}),
		slog.Any("mac", net.HardwareAddr{0x00, 0x00, 0x5e, 0x00, 0x53, 0x01}),
		slog.Any("stringer", stringer{}),
		slog.Any("marshaller", &marshaller{}),
		slog.Any("marshaller-err", &marshaller{err: errors.New("failure")}),
		slog.Any("unknown", unknown{Foo: "bar"}),
		slog.Any("json", &jsoner{foo: "bar"}),
		slog.Any("json-err", &jsoner{err: errors.New("failure")}),
	}

	exp = map[string]any{
		"titi":           "toto",
		"tata":           "tutu",
		"foo":            12.0,
		"bar":            42.0,
		"dur":            3000.0,
		"bool":           true,
		"float":          23.7,
		"thetime":        now.Format(time.RFC3339),
		"err":            "yo",
		"group":          map[string]any{"bar": "baz"},
		"ip":             "192.168.1.2",
		"ipnet":          "192.168.1.0/24",
		"mac":            "00:00:5e:00:53:01",
		"stringer":       "stringer",
		"marshaller":     "marshaller",
		"marshaller-err": "!ERROR:failure",
		"unknown":        map[string]any{"Foo": "bar"},
		"json":           map[string]any{"foo": "bar"},
		"json-err":       "!ERROR:failure",
	}

	levels = []struct {
		zlvl Level
		slvl slog.Level
	}{
		{TraceLevel, slog.LevelDebug - 1},
		{DebugLevel, slog.LevelDebug},
		{InfoLevel, slog.LevelInfo},
		{WarnLevel, slog.LevelWarn},
		{WarnLevel, slog.LevelWarn + 1},
		{WarnLevel, slog.LevelError - 1},
		{ErrorLevel, slog.LevelError},
		{ErrorLevel, slog.LevelError + 1},
	}
)

func NewJSONHandler(out io.Writer, opts *SlogHandlerOptions) *SlogHandler {
	return NewSlogHandler(New(out).Level(InfoLevel), opts)
}

func decode(b *bytes.Buffer) (map[string]any, error) {
	m := make(map[string]any)
	s := decodeIfBinaryToString(b.Bytes())
	err := json.NewDecoder(strings.NewReader(s)).Decode(&m)
	if err == nil {
		b.Reset()
	}
	return m, err
}

func mustDecode(t *testing.T, b *bytes.Buffer) map[string]any {
	t.Helper()
	m, err := decode(b)
	if err != nil {
		t.Fatalf("Failed to json decode log output: %s", err.Error())
	}
	return m
}

func TestZerolog_Levels(t *testing.T) {
	out := bytes.Buffer{}
	for _, lvl := range levels {
		t.Run(lvl.slvl.String(), func(t *testing.T) {
			hdl := NewJSONHandler(&out, &SlogHandlerOptions{Level: lvl.slvl})
			for _, l := range levels {
				enabled := l.slvl >= lvl.slvl
				if hdl.Enabled(nil, l.slvl) != enabled {
					t.Fatalf("Level %s enablement status unexpected", l.slvl)
				}
				hdl.Handle(nil, slog.NewRecord(time.Now(), l.slvl, "foobar", 0))
				if enabled {
					m := mustDecode(t, &out)
					if m[LevelFieldName] != l.zlvl.String() {
						t.Fatalf("Unexpected value for field %s. Got %s but expected %s", LevelFieldName, m[LevelFieldName], l.zlvl.String())
					}
				}
				out.Reset()
			}
		})
	}
}

func TestZerolog_Levels_NoOption(t *testing.T) {
	out := bytes.Buffer{}
	for _, lvl := range levels {
		t.Run(lvl.slvl.String(), func(t *testing.T) {
			hdl := NewSlogHandler(New(&out).Level(lvl.zlvl), nil)
			for _, l := range levels {
				enabled := l.zlvl >= lvl.zlvl
				if hdl.Enabled(nil, l.slvl) != enabled {
					t.Fatalf("Level %s enablement status unexpected", l.slvl)
				}
				hdl.Handle(nil, slog.NewRecord(time.Now(), l.slvl, "foobar", 0))
				m, err := decode(&out)
				if enabled {
					if err != nil {
						t.Fatalf("Failed to json decode log output: %s", err.Error())
					}
					if m[LevelFieldName] != l.zlvl.String() {
						t.Fatalf("Unexpected value for field %s. Got %s but expected %s", LevelFieldName, m[LevelFieldName], l.zlvl.String())
					}
				} else {
					if !errors.Is(err, io.EOF) {
						t.Fatalf("Expected io.EOF error but got %s", err)
					}
				}
				out.Reset()
			}

		})
	}
}

func TestZerolog_NoGroup(t *testing.T) {
	out := bytes.Buffer{}
	hdl := NewJSONHandler(&out, nil).
		WithAttrs([]slog.Attr{slog.String("attr", "the attr")})

	if !hdl.Enabled(nil, slog.LevelError) {
		t.Errorf("Level %s must be enabled", slog.LevelError)
	}
	if hdl.Enabled(nil, slog.LevelDebug) {
		t.Errorf("Level %s must be disabled", slog.LevelDebug)
	}

	rec := slog.NewRecord(now, slog.LevelError, "foobar", 0)
	rec.AddAttrs(attrs...)
	hdl.Handle(nil, rec)

	expected := maps.Clone(exp)
	expected[LevelFieldName] = LevelErrorValue
	expected[MessageFieldName] = "foobar"
	expected[TimestampFieldName] = now.Format(time.RFC3339)
	expected["attr"] = "the attr"

	m := mustDecode(t, &out)
	if !reflect.DeepEqual(expected, m) {
		t.Fatalf("Unexpected fields. Got %v, expected %v", m, expected)
	}
}

func TestZerolog_Group(t *testing.T) {
	out := bytes.Buffer{}
	hdl := NewJSONHandler(&out, nil).
		WithAttrs([]slog.Attr{slog.String("attr", "the attr")}).
		WithGroup("testgroup").
		WithAttrs([]slog.Attr{slog.String("attr", "the attr")}).
		WithGroup("subgroup")

	if !hdl.Enabled(nil, slog.LevelError) {
		t.Errorf("Level %s must be enabled", slog.LevelError)
	}
	if hdl.Enabled(nil, slog.LevelDebug) {
		t.Errorf("Level %s must be disabled", slog.LevelDebug)
	}

	rec := slog.NewRecord(now, slog.LevelWarn, "foobar", 0)
	rec.AddAttrs(attrs...)
	hdl.Handle(nil, rec)

	expected := map[string]any{
		LevelFieldName:     LevelWarnValue,
		MessageFieldName:   "foobar",
		TimestampFieldName: now.Format(time.RFC3339),
		"attr":             "the attr",
		"testgroup": map[string]any{
			"attr":     "the attr",
			"subgroup": maps.Clone(exp),
		},
	}

	m := mustDecode(t, &out)
	if !reflect.DeepEqual(expected, m) {
		t.Fatalf("Unexpected fields. \nGot %v,\n expected %v", m, expected)
	}
}

func TestZerolog_AddSource(t *testing.T) {
	out := bytes.Buffer{}
	hdl := NewJSONHandler(&out, &SlogHandlerOptions{AddSource: true})
	pc, file, line, _ := runtime.Caller(0)
	hdl.Handle(context.Background(), slog.NewRecord(time.Now(), slog.LevelInfo, "foobar", pc))
	m := mustDecode(t, &out)
	if m[CallerFieldName].(string) != fmt.Sprintf("%s:%d", file, line) {
		t.Fatalf("Unexpected field %s: %s", CallerFieldName, m[CallerFieldName].(string))
	}
}

// TestHandler uses slogtest.TestHandler from stdlib to validate
// the zerolog handler implementation.
func TestHandler(t *testing.T) {
	out := bytes.Buffer{}
	dec := json.NewDecoder(&out)
	hdl := NewJSONHandler(&out, &SlogHandlerOptions{Level: slog.LevelDebug})
	err := slogtest.TestHandler(hdl, func() []map[string]any {
		results := []map[string]any{}
		m := map[string]any{}
		for dec.Decode(&m) != io.EOF {
			results = append(results, m)
			m = map[string]any{}
		}
		return results
	})
	if err != nil {
		t.Fatal(err)
	}
}
