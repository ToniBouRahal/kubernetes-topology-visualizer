package query

import (
	"strings"
	"testing"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

var now = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// T-15.3: presets map to the API's window parameter.
func TestSincePresetsMapToWindow(t *testing.T) {
	for _, p := range api.Presets {
		w, err := Since(p, now)
		if err != nil || w != (api.Window{Preset: p}) {
			t.Errorf("Since(%q) = %+v, %v; want preset", p, w, err)
		}
	}
}

// T-15.3: any other duration becomes [now − D, now) in UTC.
func TestSinceOtherDurationsBecomeExplicitUTC(t *testing.T) {
	local := now.In(time.FixedZone("UTC+3", 3*3600))
	w, err := Since("2h", local)
	if err != nil {
		t.Fatal(err)
	}
	if w.Preset != "" || !w.From.Equal(mustTime(t, "2026-08-10T10:00:00Z")) || !w.To.Equal(now) {
		t.Fatalf("Since(2h) = %+v", w)
	}
	if w.From.Location() != time.UTC || w.To.Location() != time.UTC {
		t.Errorf("window is not UTC: %v %v", w.From.Location(), w.To.Location())
	}
	if w, _ := Since("", now); w != (api.Window{}) {
		t.Errorf("empty --since = %+v, want the zero window (server default)", w)
	}
	for _, bad := range []string{"2", "-1h", "0s", "yesterday"} {
		if _, err := Since(bad, now); err == nil {
			t.Errorf("Since(%q) accepted", bad)
		}
	}
}

func TestInstantForms(t *testing.T) {
	for in, want := range map[string]string{
		"now":                       "2026-08-10T12:00:00Z",
		"NOW":                       "2026-08-10T12:00:00Z",
		"90m":                       "2026-08-10T10:30:00Z",
		"2026-08-10T09:00:00Z":      "2026-08-10T09:00:00Z",
		"2026-08-10T11:00:00+02:00": "2026-08-10T09:00:00Z",
	} {
		got, err := Instant(in, now)
		if err != nil || !got.Equal(mustTime(t, want)) || got.Location() != time.UTC {
			t.Errorf("Instant(%q) = %v, %v; want %s UTC", in, got, err, want)
		}
	}
	if _, err := Instant("2026-08-10 09:00", now); err == nil {
		t.Error("a non-RFC 3339 time was accepted")
	}
}

func TestWindowFlagsAreExclusive(t *testing.T) {
	if _, err := Window("1h", "2h", "now", now); err == nil {
		t.Error("--since with --from/--to accepted")
	}
	if _, err := Window("", "2h", "", now); err == nil {
		t.Error("--from without --to accepted")
	}
	if _, err := Window("", "now", "2h", now); err == nil {
		t.Error("--from after --to accepted")
	}
	w, err := Window("", "2h", "1h", now)
	if err != nil || !w.From.Equal(mustTime(t, "2026-08-10T10:00:00Z")) || !w.To.Equal(mustTime(t, "2026-08-10T11:00:00Z")) {
		t.Errorf("Window(--from 2h --to 1h) = %+v, %v", w, err)
	}
}

// T-15.3: "diff --from 2h --to now" is two adjacent, equal one-hour periods.
func TestDiffFrom2hToNowIsTwoAdjacentHours(t *testing.T) {
	b, c, err := DiffPeriods("2h", "now", "", now)
	if err != nil {
		t.Fatal(err)
	}
	want := [4]string{"2026-08-10T10:00:00Z", "2026-08-10T11:00:00Z", "2026-08-10T11:00:00Z", "2026-08-10T12:00:00Z"}
	got := [4]time.Time{b.From, b.To, c.From, c.To}
	for i := range want {
		if !got[i].Equal(mustTime(t, want[i])) {
			t.Fatalf("periods = %v, want %v", got, want)
		}
	}
	if b.To.Sub(b.From) != time.Hour || c.To.Sub(c.From) != time.Hour || !b.To.Equal(c.From) {
		t.Errorf("periods not adjacent and equal: %v %v", b, c)
	}
}

func TestDiffLengthLeavesAGapAndOverlapIsRefused(t *testing.T) {
	b, c, err := DiffPeriods("2h", "now", "15m", now)
	if err != nil {
		t.Fatal(err)
	}
	if b.To.Sub(b.From) != 15*time.Minute || c.To.Sub(c.From) != 15*time.Minute ||
		!b.From.Equal(mustTime(t, "2026-08-10T10:00:00Z")) || !c.To.Equal(now) {
		t.Errorf("--length 15m gave %v %v", b, c)
	}

	_, _, err = DiffPeriods("2h", "now", "90m", now)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Errorf("overlapping periods: err = %v, want refusal", err)
	}
	for _, args := range [][3]string{{"now", "2h", ""}, {"", "now", ""}, {"2h", "now", "-5m"}} {
		if _, _, err := DiffPeriods(args[0], args[1], args[2], now); err == nil {
			t.Errorf("DiffPeriods%q accepted", args)
		}
	}
}
