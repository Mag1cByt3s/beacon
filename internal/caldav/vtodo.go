package caldav

import (
	"strings"
	"time"

	"github.com/emersion/go-ical"

	"github.com/Mag1cByt3s/beacon/internal/focus"
)

// taskFromCalendar turns one calendar object into a task. ok is false if the
// object holds no VTODO or the task is already completed or cancelled.
//
// A recurring task can be stored as one master VTODO plus overrides that
// carry a RECURRENCE-ID. The master describes the task; any RRULE or
// RECURRENCE-ID marks it as recurring.
func taskFromCalendar(cal *ical.Calendar, list string) (task focus.Task, ok bool) {
	var master *ical.Component
	recurring := false
	for _, comp := range cal.Children {
		if comp.Name != ical.CompToDo {
			continue
		}
		hasRecurrenceID := comp.Props.Get(ical.PropRecurrenceID) != nil
		if comp.Props.Get(ical.PropRecurrenceRule) != nil || hasRecurrenceID {
			recurring = true
		}
		if master == nil || (!hasRecurrenceID && master.Props.Get(ical.PropRecurrenceID) != nil) {
			master = comp
		}
	}
	if master == nil || !isOpen(master) {
		return focus.Task{}, false
	}

	props := master.Props
	task = focus.Task{
		UID:       text(props, ical.PropUID),
		Summary:   strings.TrimSpace(text(props, ical.PropSummary)),
		List:      list,
		Recurring: recurring,
	}
	if task.Summary == "" {
		task.Summary = "(no title)"
	}
	task.Due, task.DueAllDay = dateTime(props.Get(ical.PropDue))
	task.Created, _ = dateTime(props.Get(ical.PropCreated))
	if p := props.Get(ical.PropPriority); p != nil {
		// A broken PRIORITY is treated as "no priority" rather than an error.
		task.Priority, _ = p.Int()
	}
	return task, true
}

// isOpen reports whether a VTODO still needs doing. STATUS decides if it is
// set; otherwise a COMPLETED timestamp means the task is done.
func isOpen(todo *ical.Component) bool {
	switch strings.ToUpper(text(todo.Props, ical.PropStatus)) {
	case "COMPLETED", "CANCELLED":
		return false
	case "":
		return todo.Props.Get(ical.PropCompleted) == nil
	default: // NEEDS-ACTION, IN-PROCESS
		return true
	}
}

// text returns a text property's value, or "" if it is missing or invalid.
func text(props ical.Props, name string) string {
	s, err := props.Text(name)
	if err != nil {
		return ""
	}
	return s
}

// dateTime parses a DATE or DATE-TIME property. It returns the zero time if
// the property is missing or cannot be parsed, so one odd task never breaks
// the whole list. allDay is true for a plain DATE.
//
// Times without a time zone ("floating") and dates are read in local time.
func dateTime(prop *ical.Prop) (t time.Time, allDay bool) {
	if prop == nil {
		return time.Time{}, false
	}
	// Some clients write a plain date without VALUE=DATE, so check the
	// length too. Go layouts use the reference date 2006-01-02.
	const dateLayout = "20060102"
	if prop.ValueType() == ical.ValueDate || len(prop.Value) == len(dateLayout) {
		date, err := time.ParseInLocation(dateLayout, prop.Value, time.Local)
		if err != nil {
			return time.Time{}, false
		}
		return date, true
	}

	t, err := prop.DateTime(time.Local)
	if err != nil && prop.Params.Get(ical.ParamTimezoneID) != "" {
		// Unknown time zone name (for example a Windows one): retry as
		// local time. Work on a copy so the original is left untouched.
		local := *prop
		local.Params = ical.Params{}
		for name, values := range prop.Params {
			if name != ical.ParamTimezoneID {
				local.Params[name] = values
			}
		}
		t, err = local.DateTime(time.Local)
	}
	if err != nil {
		return time.Time{}, false
	}
	return t, false
}
