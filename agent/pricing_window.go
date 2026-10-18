package agent

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yusiwen/tinycode/types"
)

// endOfDay is midnight at the end of a day: a minute count rather than a clock
// reading, so 1440 is where a window ending at midnight stops.
const endOfDay = 24 * 60

// PriceWindow is a slice of the week during which one entry charges rates of its
// own.
//
// The entry's own rates stay the price outside every window, so a route that is
// cheap most of the week declares the cheap rates on the entry and the expensive
// slice as a window. That is the smaller of the two declarations, and it needs no
// rule for what an uncovered minute costs: it costs the entry's rates.
//
// A window is (weekdays × hours) rather than a list of instants, because that is
// how a provider states its schedule and how few numbers can express it: peak
// hours are 01:00-04:00 and 06:00-10:00 on weekdays, not 35 hourly entries.
type PriceWindow struct {
	// weekdays is a bit set over time.Weekday, so bit 0 is Sunday.
	weekdays uint8
	// from and to are minutes since midnight, UTC. from is inclusive and to is
	// exclusive, which is what makes 04:00 belong to the rate after the one that
	// ends at 04:00 — the boundary minute is the one on the clock.
	from, to int
	price    Price
}

// NewWindow parses one declared window.
//
// weekdays accepts the three-letter or full English name of a day, in any case;
// from and to accept HH:MM in UTC, and 00:00 or 24:00 as the end of a day, because
// that is how the published schedules write "until midnight" (LiteLLM's DeepSeek
// windows say 10:00-00:00). A window that crosses midnight is refused rather than
// guessed at: there is no single weekday for its minutes, and the fix — two
// windows — says the same thing unambiguously.
//
// Every refusal names what the reader wrote and what would be accepted, because a
// price schedule that is silently misread is money.
func NewWindow(weekdays []string, from, to string, price Price) (PriceWindow, error) {
	label := fmt.Sprintf("window %q", windowLabel(weekdays, from, to))

	days, err := parseWeekdays(weekdays)
	if err != nil {
		return PriceWindow{}, fmt.Errorf("%s: %w", label, err)
	}
	start, err := parseClock(from)
	if err != nil {
		return PriceWindow{}, fmt.Errorf("%s: start %w", label, err)
	}
	end, err := parseClock(to)
	if err != nil {
		return PriceWindow{}, fmt.Errorf("%s: end %w", label, err)
	}
	if end == 0 {
		end = endOfDay
	}
	if start >= endOfDay {
		return PriceWindow{}, fmt.Errorf("%s: starts at 24:00, which is the end of the day rather than a start", label)
	}
	if end == start {
		return PriceWindow{}, fmt.Errorf("%s: starts and ends at %s, so it covers no time", label, clockLabel(start))
	}
	if end < start {
		return PriceWindow{}, fmt.Errorf("%s: ends at %s, before it starts at %s; a window that crosses midnight must be written as two windows",
			label, clockLabel(end), clockLabel(start))
	}
	return PriceWindow{weekdays: days, from: start, to: end, price: price}, nil
}

// covers reports whether the window applies at an instant. Seconds are ignored:
// windows are whole minutes, and a call made at 03:59:59 belongs to a window that
// ends at 04:00.
func (w PriceWindow) covers(at time.Time) bool {
	if w.weekdays&(1<<uint(at.Weekday())) == 0 {
		return false
	}
	minute := at.Hour()*60 + at.Minute()
	return minute >= w.from && minute < w.to
}

// describe names the window as a schedule, for a refusal that has to point at the
// offender.
func (w PriceWindow) describe() string {
	return fmt.Sprintf("%s %s-%s", formatWeekdays(w.weekdays), clockLabel(w.from), clockLabel(w.to))
}

// windowLabel shows a declaration as its author wrote it, for an error raised
// before it could be parsed.
func windowLabel(weekdays []string, from, to string) string {
	return strings.Join(weekdays, "+") + " " + from + "-" + to
}

// SetEntry declares one key's rates and the windows during which other rates
// apply.
//
// The whole set is validated before anything is stored, so a refused declaration
// leaves no half-priced entry behind: an entry that overlaps or leaves a lane
// unpriced prices nothing at all, which the callers report as an unknown cost
// rather than as a number nobody can defend.
//
// With no windows this is Set plus validation, and it behaves exactly as before.
func (t *PriceTable) SetEntry(key string, base Price, windows []PriceWindow) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("a price entry needs a key of the form <route>/<model>")
	}
	if err := validateWindowSet(base, windows); err != nil {
		return fmt.Errorf("pricing %s: %w", key, err)
	}
	if t == nil {
		return nil
	}
	if t.prices == nil {
		t.prices = map[string]Price{}
	}
	t.prices[key] = base
	if len(windows) == 0 {
		// A raw rate replaces an entry that had windows: the caller declared what
		// the route charges, and keeping an older schedule beside it would apply a
		// rate nobody asked for.
		delete(t.windows, key)
		return nil
	}
	if t.windows == nil {
		t.windows = map[string][]PriceWindow{}
	}
	t.windows[key] = windows
	return nil
}

// AmountAt prices one usage record at an instant, or reports that no declared
// rate covers the call.
//
// The instant is the start of the provider call, not the moment its usage was
// recorded: a route bills by the hour the request was made, and a long stream can
// cross the boundary while it is being generated.
func (t *PriceTable) AmountAt(key string, usage types.Usage, at time.Time) (types.Cost, bool) {
	price, ok := t.lookupAt(key, at)
	if !ok {
		return types.Cost{}, false
	}
	return types.Cost{Amount: price.amount(usage), Currency: t.Currency}, true
}

// lookupAt resolves the key as Lookup does, then narrows the entry it found to the
// rate that applies at that instant. The schedule consulted is the winning entry's
// own: a wildcard's windows never reach a call that an exact key priced.
func (t *PriceTable) lookupAt(key string, at time.Time) (Price, bool) {
	if t == nil {
		return Price{}, false
	}
	route, model, ok := splitPriceKey(key)
	if !ok {
		return Price{}, false
	}
	for _, candidate := range []string{
		route + "/" + model,
		route + "/*",
		"*/" + model,
		"*/*",
	} {
		base, found := t.prices[candidate]
		if !found {
			continue
		}
		for _, w := range t.windows[candidate] {
			if w.covers(at.UTC()) {
				return w.price, true
			}
		}
		return base, true
	}
	return Price{}, false
}

// validateWindowSet refuses a declaration that cannot be priced unambiguously.
//
// The rules are the ones a reader would ask for: one rate per minute, the entry's
// own rates still reachable, and every lane the entry declares also declared
// inside the window.
func validateWindowSet(base Price, windows []PriceWindow) error {
	for _, w := range windows {
		if w.weekdays == 0 || w.from < 0 || w.to > endOfDay || w.to <= w.from {
			return fmt.Errorf("%s does not describe a slice of the week", w.describe())
		}
		if missing := lanesTheWindowLeavesZero(base, w.price); len(missing) > 0 {
			return fmt.Errorf("%s leaves %s at zero, but the entry declares it; repeat every lane the entry declares, because a zero lane would silently charge the entry's rate inside the window",
				w.describe(), strings.Join(missing, ", "))
		}
	}

	for day := time.Sunday; day <= time.Saturday; day++ {
		var covering []PriceWindow
		for _, w := range windows {
			if w.weekdays&(1<<uint(day)) != 0 {
				covering = append(covering, w)
			}
		}
		sort.SliceStable(covering, func(i, j int) bool { return covering[i].from < covering[j].from })
		for i := 1; i < len(covering); i++ {
			if covering[i].from < covering[i-1].to {
				return fmt.Errorf("%s and %s overlap on %s, so a call in the shared minutes has no single price",
					covering[i-1].describe(), covering[i].describe(), weekdayLabel(day))
			}
		}
	}

	if coversWholeWeek(windows) {
		return fmt.Errorf("the windows cover every minute of the week, so the entry's own rates could never apply; declare the rates outside the windows on the entry instead")
	}
	return nil
}

// lanesTheWindowLeavesZero names the lanes the entry declares and the window does
// not, spelled as the configuration file spells them, because that is the line the
// reader has to edit.
func lanesTheWindowLeavesZero(base, window Price) []string {
	var missing []string
	for _, lane := range []struct {
		name         string
		base, window float64
	}{
		{"input_per_million", base.InputPerMillion, window.InputPerMillion},
		{"output_per_million", base.OutputPerMillion, window.OutputPerMillion},
		{"cache_read_per_million", base.CacheReadPerMillion, window.CacheReadPerMillion},
		{"cache_write_per_million", base.CacheWritePerMillion, window.CacheWritePerMillion},
		{"reasoning_per_million", base.ReasoningPerMillion, window.ReasoningPerMillion},
	} {
		if lane.base != 0 && lane.window == 0 {
			missing = append(missing, lane.name)
		}
	}
	return missing
}

// coversWholeWeek reports whether every weekday is covered from 00:00 to 24:00.
// Overlaps have already been refused, so walking each day in order is enough.
func coversWholeWeek(windows []PriceWindow) bool {
	for day := time.Sunday; day <= time.Saturday; day++ {
		var starts []PriceWindow
		for _, w := range windows {
			if w.weekdays&(1<<uint(day)) != 0 {
				starts = append(starts, w)
			}
		}
		sort.SliceStable(starts, func(i, j int) bool { return starts[i].from < starts[j].from })
		cursor := 0
		for _, w := range starts {
			if w.from != cursor {
				cursor = -1
				break
			}
			cursor = w.to
		}
		if cursor != endOfDay {
			return false
		}
	}
	return true
}

// parseWeekdays turns day names into the bit set the window keeps.
func parseWeekdays(names []string) (uint8, error) {
	if len(names) == 0 {
		return 0, fmt.Errorf("no weekdays; name at least one of mon, tue, wed, thu, fri, sat, sun")
	}
	var days uint8
	for _, name := range names {
		day, ok := weekdayByName(strings.ToLower(strings.TrimSpace(name)))
		if !ok {
			return 0, fmt.Errorf("unknown weekday %q; name days as mon, tue, wed, thu, fri, sat, sun (or their full names)", name)
		}
		days |= 1 << uint(day)
	}
	return days, nil
}

func weekdayByName(name string) (time.Weekday, bool) {
	switch name {
	case "sun", "sunday":
		return time.Sunday, true
	case "mon", "monday":
		return time.Monday, true
	case "tue", "tues", "tuesday":
		return time.Tuesday, true
	case "wed", "weds", "wednesday":
		return time.Wednesday, true
	case "thu", "thur", "thurs", "thursday":
		return time.Thursday, true
	case "fri", "friday":
		return time.Friday, true
	case "sat", "saturday":
		return time.Saturday, true
	}
	return time.Sunday, false
}

// parseClock turns HH:MM into minutes since midnight. It accepts 24:00, which is
// the end of the day rather than a clock reading, so a published schedule can be
// copied as it is written.
func parseClock(text string) (int, error) {
	trimmed := strings.TrimSpace(text)
	hourText, minuteText, ok := strings.Cut(trimmed, ":")
	if !ok || !digits(hourText) || !digits(minuteText) || len(hourText) > 2 || len(minuteText) != 2 {
		return 0, fmt.Errorf("time %q is not HH:MM in UTC", text)
	}
	hour, _ := strconv.Atoi(hourText)
	minute, _ := strconv.Atoi(minuteText)
	if hour > 24 || minute > 59 || (hour == 24 && minute != 0) {
		return 0, fmt.Errorf("time %q is not a time of day", text)
	}
	return hour*60 + minute, nil
}

// digits reports whether s is one or more ASCII digits and nothing else. Atoi
// alone would accept a sign, which is not a clock reading.
func digits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// clockLabel writes a minute count back as HH:MM, with 24:00 for the end of a day.
func clockLabel(minutes int) string {
	return fmt.Sprintf("%02d:%02d", minutes/60, minutes%60)
}

// weekdayLabel names one day in three letters.
func weekdayLabel(day time.Weekday) string {
	return [...]string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[day]
}

// formatWeekdays writes the bit set as a schedule, in week order, collapsing a run
// of days into a range: mon-fri, sat, mon+wed+fri, every day.
func formatWeekdays(days uint8) string {
	const all = uint8(1<<7) - 1
	if days == all {
		return "every day"
	}
	order := []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday}
	var parts []string
	for i := 0; i < len(order); i++ {
		if days&(1<<uint(order[i])) == 0 {
			continue
		}
		j := i
		for j+1 < len(order) && days&(1<<uint(order[j+1])) != 0 {
			j++
		}
		if j == i {
			parts = append(parts, weekdayLabel(order[i]))
		} else {
			parts = append(parts, weekdayLabel(order[i])+"-"+weekdayLabel(order[j]))
		}
		i = j
	}
	return strings.Join(parts, "+")
}
