// Package query turns what a user types — durations, times, a workload name — into API
// parameters (ADR-015 D-15.4). It is shared by the CLI commands and the MCP tools so both
// interpret "--since 1h" and "backend" identically.
package query

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/fyp/kubernetes-topology-visualizer/cli/internal/api"
)

// Since turns "--since D" into a window: an API preset when D is spelled as one, otherwise
// [now − D, now) as RFC 3339 UTC. An empty D is the zero Window — the backend's default applies
// and the response's window reports it.
func Since(d string, now time.Time) (api.Window, error) {
	d = strings.TrimSpace(d)
	if d == "" {
		return api.Window{}, nil
	}
	if slices.Contains(api.Presets, d) {
		return api.Window{Preset: d}, nil
	}
	dur, err := parseAgo(d)
	if err != nil {
		return api.Window{}, fmt.Errorf("--since %q: %w", d, err)
	}
	now = now.UTC()
	return api.Window{From: now.Add(-dur), To: now}, nil
}

// Instant reads a --from/--to value: "now", a duration ago ("2h"), or an RFC 3339 time with any
// offset, returned in UTC.
func Instant(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return time.Time{}, errors.New("empty time: want now, a duration ago such as 2h, or RFC 3339")
	case strings.EqualFold(s, "now"):
		return now.UTC(), nil
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t.UTC(), nil
	}
	dur, err := parseAgo(s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not now, a duration ago such as 2h, or an RFC 3339 time", s)
	}
	return now.UTC().Add(-dur), nil
}

// Range turns "--from A --to B" into an explicit window. Both are required, and A must be
// before B.
func Range(from, to string, now time.Time) (api.Window, error) {
	if from == "" || to == "" {
		return api.Window{}, errors.New("--from and --to must be given together")
	}
	a, err := Instant(from, now)
	if err != nil {
		return api.Window{}, fmt.Errorf("--from: %w", err)
	}
	b, err := Instant(to, now)
	if err != nil {
		return api.Window{}, fmt.Errorf("--to: %w", err)
	}
	if !a.Before(b) {
		return api.Window{}, fmt.Errorf("--from (%s) must be before --to (%s)", format(a), format(b))
	}
	return api.Window{From: a, To: b}, nil
}

// Window picks between --since and --from/--to, which are mutually exclusive.
func Window(since, from, to string, now time.Time) (api.Window, error) {
	if since != "" && (from != "" || to != "") {
		return api.Window{}, errors.New("--since cannot be combined with --from/--to")
	}
	if from != "" || to != "" {
		return Range(from, to, now)
	}
	return Since(since, now)
}

// DiffPeriods turns "diff --from A --to B [--length L]" into two equal-length periods
// (limitations §4.2): baseline [A, A+L) and current [B−L, B). L defaults to half of B − A, which
// makes the periods adjacent; a longer L would make them overlap, and that is refused — an
// observation counted in both periods would show as unchanged when it is not.
func DiffPeriods(from, to, length string, now time.Time) (baseline, current api.Period, err error) {
	if from == "" || to == "" {
		return api.Period{}, api.Period{}, errors.New("diff needs both --from and --to")
	}
	a, err := Instant(from, now)
	if err != nil {
		return api.Period{}, api.Period{}, fmt.Errorf("--from: %w", err)
	}
	b, err := Instant(to, now)
	if err != nil {
		return api.Period{}, api.Period{}, fmt.Errorf("--to: %w", err)
	}
	if !a.Before(b) {
		return api.Period{}, api.Period{}, fmt.Errorf("--from (%s) must be before --to (%s)", format(a), format(b))
	}
	l := b.Sub(a) / 2
	if length != "" {
		if l, err = parseAgo(length); err != nil {
			return api.Period{}, api.Period{}, fmt.Errorf("--length %q: %w", length, err)
		}
	}
	baseline = api.Period{From: a, To: a.Add(l)}
	current = api.Period{From: b.Add(-l), To: b}
	if baseline.To.After(current.From) {
		return api.Period{}, api.Period{}, fmt.Errorf(
			"periods overlap: baseline %s to %s and current %s to %s; use a --length of at most %s",
			format(baseline.From), format(baseline.To), format(current.From), format(current.To), b.Sub(a)/2)
	}
	return baseline, current, nil
}

// parseAgo reads a positive Go duration ("90s", "15m", "2h30m").
func parseAgo(s string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(s))
	if err != nil {
		return 0, errors.New("want a duration such as 15m, 1h or 2h30m")
	}
	if d <= 0 {
		return 0, errors.New("duration must be positive")
	}
	return d, nil
}

func format(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
