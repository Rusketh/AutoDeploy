package logging

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func TestEventLevelFollowsAction(t *testing.T) {
	cases := map[string]slog.Level{
		"software.upload.file_ok":      slog.LevelInfo,
		"software.upload.rejected":     slog.LevelWarn,
		"software.upload.stream_error": slog.LevelWarn,
		"software.upload.bad_request":  slog.LevelWarn,
		"software.upload.write_failed": slog.LevelError,
		"retention.log_prune.fail":     slog.LevelWarn,
	}
	for action, want := range cases {
		var buf bytes.Buffer
		ctx := With(context.Background(), New(&buf, "test"))
		var got slog.Level
		SetSink(func(_ context.Context, l slog.Level, a, actor, target string, _ []slog.Attr) {
			if a != action || actor != "admin" || target != "software:1" {
				t.Errorf("sink got %s %s %s", a, actor, target)
			}
			got = l
		})
		Event(ctx, action, "admin", "software:1", slog.String("file", "x.msi"))
		SetSink(nil)
		if got != want {
			t.Errorf("%s: sink level %v, want %v", action, got, want)
		}
		if !strings.Contains(buf.String(), `"level":"`+want.String()+`"`) {
			t.Errorf("%s: logged %s, want level %v", action, buf.String(), want)
		}
	}
}
