package server

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Mag1cByt3s/beacon/internal/api"
	"github.com/Mag1cByt3s/beacon/internal/journal"
)

// The journal endpoints. They keep their data in beacon's own database;
// only the suggestion also reads tasks from CalDAV.

// maxBody limits request bodies; a journal day is a few kilobytes at most.
const maxBody = 64 << 10

// date returns the journal day named in the request path ("today" or a
// date), or answers 400 and returns ok == false.
func (s *Server) date(w http.ResponseWriter, r *http.Request) (string, bool) {
	date, err := s.Clock.Resolve(r.PathValue("date"), s.now())
	if err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return "", false
	}
	return date, true
}

func journalResponse(e journal.Entry) api.JournalResponse {
	return api.JournalResponse{Entry: e, Missing: e.Missing()}
}

func (s *Server) getJournal(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	e, _, err := s.store.Entry(r.Context(), date)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, journalResponse(e))
}

// patchJournal changes the fields the body mentions and keeps the others:
// the body is decoded onto the stored entry (see journal.Entry).
func (s *Server) patchJournal(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}

	s.journalMu.Lock()
	defer s.journalMu.Unlock()

	e, _, err := s.store.Entry(r.Context(), date)
	if err != nil {
		s.fail(w, err)
		return
	}
	if err := decodeStrict(body, &e); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	if err := s.saveEntry(r, date, e); err != nil {
		s.failJournal(w, err)
		return
	}
	s.getJournal(w, r)
}

// putAW stores what ActivityWatch measured for the day. Only the client
// aw-sync may call it.
func (s *Server) putAW(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req api.AWRequest
	if err := decodeStrict(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	for i := range req.Windows {
		if req.Windows[i].Kind == "" {
			req.Windows[i].Kind = journal.KindAW
		}
	}

	s.journalMu.Lock()
	defer s.journalMu.Unlock()

	e, _, err := s.store.Entry(r.Context(), date)
	if err != nil {
		s.fail(w, err)
		return
	}
	e.AWProductiveMinutes = &req.Minutes
	e.AWWindows = req.Windows
	if e.AWWindows == nil {
		e.AWWindows = []journal.Window{}
	}
	if err := s.saveEntry(r, date, e); err != nil {
		s.failJournal(w, err)
		return
	}
	s.getJournal(w, r)
}

// saveEntry validates e and stores it as the entry for date, marked as
// changed now by the client of the request.
func (s *Server) saveEntry(r *http.Request, date string, e journal.Entry) error {
	e.Date = date // the path decides, not the body
	if err := e.Validate(); err != nil {
		return badRequest{err}
	}
	e.UpdatedAt = s.now()
	e.UpdatedBy = clientName(r)
	if err := s.store.SaveEntry(r.Context(), e); err != nil {
		return err
	}
	s.log.Info("journal saved", "date", date, "client", e.UpdatedBy)
	return nil
}

func (s *Server) listJournal(w http.ResponseWriter, r *http.Request) {
	from, to, ok := s.dateRange(w, r)
	if !ok {
		return
	}
	entries, err := s.store.Entries(r.Context(), from, to)
	if err != nil {
		s.fail(w, err)
		return
	}
	resp := api.JournalListResponse{From: from, To: to, Entries: []api.JournalResponse{}}
	for _, e := range entries {
		resp.Entries = append(resp.Entries, journalResponse(e))
	}
	writeJSON(w, http.StatusOK, resp)
}

// dateRange reads the from= and to= query parameters ("today" works). To
// defaults to today and from to 30 days before to.
func (s *Server) dateRange(w http.ResponseWriter, r *http.Request) (from, to string, ok bool) {
	q := r.URL.Query()
	to, from = q.Get("to"), q.Get("from")
	if to == "" {
		to = "today"
	}
	var err error
	if to, err = s.Clock.Resolve(to, s.now()); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "to: " + err.Error()})
		return "", "", false
	}
	if from == "" {
		end, _ := time.Parse(journal.DateLayout, to)
		from = end.AddDate(0, 0, -29).Format(journal.DateLayout)
	}
	if from, err = s.Clock.Resolve(from, s.now()); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "from: " + err.Error()})
		return "", "", false
	}
	if from > to { // dates like 2026-10-13 sort correctly as text
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "from is after to"})
		return "", "", false
	}
	return from, to, true
}

// suggestionTimeout bounds how long the suggestion waits for CalDAV; the
// tasks completed on the phone are then left out, with a warning.
const suggestionTimeout = 10 * time.Second

// suggestion proposes the evening part of a day from the focus sessions,
// the items, the tasks completed in CalDAV and ActivityWatch's values.
func (s *Server) suggestion(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	in := journal.SuggestInput{Date: date, Clock: s.Clock, Now: s.now()}
	var err error
	if in.Entry, _, err = s.store.Entry(ctx, date); err != nil {
		s.fail(w, err)
		return
	}
	if in.Sessions, err = s.store.Sessions(ctx, date, date); err != nil {
		s.fail(w, err)
		return
	}
	if in.Items, err = s.store.Items(ctx, date, date); err != nil {
		s.fail(w, err)
		return
	}

	var warnings []string
	start, end, _ := s.Clock.Bounds(date) // date was checked above
	caldavCtx, cancel := context.WithTimeout(ctx, suggestionTimeout)
	defer cancel()
	completed, err := s.tasks.Completed(caldavCtx, start, end)
	if err != nil {
		s.log.Warn("suggestion without the tasks from CalDAV", "err", err)
		warnings = append(warnings, "tasks completed on other devices are missing: "+err.Error())
	}
	for _, task := range completed {
		in.Completed = append(in.Completed, journal.CompletedTask{UID: task.UID, Title: task.Summary})
	}

	sg := journal.Suggest(in)
	sg.Warnings = warnings
	writeJSON(w, http.StatusOK, sg)
}

// ---- Items ----

func (s *Server) getItems(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	items, err := s.store.Items(r.Context(), date, date)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, api.ItemsResponse{Items: items})
}

func (s *Server) addItem(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var it journal.Item
	if err := decodeStrict(body, &it); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	it, err := s.newItem(date, it)
	if err != nil {
		s.failJournal(w, err)
		return
	}

	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	if it, err = s.store.AddItem(r.Context(), it); err != nil {
		s.fail(w, err)
		return
	}
	s.log.Info("journal item added", "date", date, "client", clientName(r))
	writeJSON(w, http.StatusCreated, it)
}

func (s *Server) replaceItems(w http.ResponseWriter, r *http.Request) {
	date, ok := s.date(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var req api.ItemsResponse
	if err := decodeStrict(body, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	items := make([]journal.Item, 0, len(req.Items))
	for i, it := range req.Items {
		it, err := s.newItem(date, it)
		if err != nil {
			s.failJournal(w, fmt.Errorf("item %d: %w", i+1, err))
			return
		}
		items = append(items, it)
	}

	s.journalMu.Lock()
	defer s.journalMu.Unlock()
	stored, err := s.store.ReplaceItems(r.Context(), date, items)
	if err != nil {
		s.fail(w, err)
		return
	}
	s.log.Info("journal items replaced", "date", date, "count", len(stored), "client", clientName(r))
	writeJSON(w, http.StatusOK, api.ItemsResponse{Items: stored})
}

// newItem checks and tidies an item sent by a client, for date.
func (s *Server) newItem(date string, it journal.Item) (journal.Item, error) {
	if err := it.Validate(); err != nil {
		return journal.Item{}, badRequest{err}
	}
	it = it.Normalize()
	it.ID, it.Date, it.CreatedAt = 0, date, s.now()
	return it, nil
}

// ---- CSV export ----

func (s *Server) exportJournal(w http.ResponseWriter, r *http.Request) {
	from, to, ok := s.dateRange(w, r)
	if !ok {
		return
	}
	entries, err := s.store.Entries(r.Context(), from, to)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows := [][]string{{
		"date", "sleep_quality", "sleep_hours", "productivity", "productive_hours",
		"productive_windows", "aw_productive_minutes", "aw_windows", "sport", "sport_type",
		"sport_minutes", "steps", "note", "updated_at", "updated_by",
	}}
	for _, e := range entries {
		rows = append(rows, []string{
			e.Date, intCell(e.SleepQuality), floatCell(e.SleepHours), intCell(e.Productivity),
			floatCell(e.ProductiveHours), windowsCell(e.ProductiveWindows), intCell(e.AWProductiveMinutes),
			windowsCell(e.AWWindows), boolCell(e.Sport), stringCell(e.SportType),
			intCell(e.SportMinutes), intCell(e.Steps), stringCell(e.Note),
			e.UpdatedAt.In(s.Clock.Loc).Format(time.RFC3339), e.UpdatedBy,
		})
	}
	writeCSV(w, fmt.Sprintf("journal-%s-%s.csv", from, to), rows)
}

func (s *Server) exportItems(w http.ResponseWriter, r *http.Request) {
	from, to, ok := s.dateRange(w, r)
	if !ok {
		return
	}
	items, err := s.store.Items(r.Context(), from, to)
	if err != nil {
		s.fail(w, err)
		return
	}
	rows := [][]string{{"date", "title", "minutes", "kind", "source", "ref_id", "created_at"}}
	for _, it := range items {
		rows = append(rows, []string{
			it.Date, it.Title, intCell(it.Minutes), it.Kind, it.Source, it.RefID,
			it.CreatedAt.In(s.Clock.Loc).Format(time.RFC3339),
		})
	}
	writeCSV(w, fmt.Sprintf("journal-items-%s-%s.csv", from, to), rows)
}

func writeCSV(w http.ResponseWriter, filename string, rows [][]string) {
	var buf bytes.Buffer
	cw := csv.NewWriter(&buf)
	cw.WriteAll(rows) // writes to memory, so it cannot fail
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Write(buf.Bytes())
}

// Cells for CSV: empty when the value is unknown.
func intCell(v *int) string {
	if v == nil {
		return ""
	}
	return strconv.Itoa(*v)
}

func floatCell(v *float64) string {
	if v == nil {
		return ""
	}
	return strconv.FormatFloat(*v, 'f', -1, 64)
}

func boolCell(v *bool) string {
	if v == nil {
		return ""
	}
	return strconv.FormatBool(*v)
}

func stringCell(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// windowsCell writes windows as "09:15-11:40 computer; 14:00-15:30 offline".
func windowsCell(ws []journal.Window) string {
	parts := make([]string, len(ws))
	for i, w := range ws {
		parts[i] = strings.TrimSpace(w.Start + "-" + w.End + " " + w.Kind)
	}
	return strings.Join(parts, "; ")
}

// ---- Helpers ----

// readBody reads a request body of at most maxBody bytes.
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	var buf bytes.Buffer
	// MaxBytesReader stops a client from sending an endless body.
	if _, err := buf.ReadFrom(http.MaxBytesReader(w, r.Body, maxBody)); err != nil {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: "the request body is too large or broken"})
		return nil, false
	}
	return buf.Bytes(), true
}

// decodeStrict decodes JSON onto v and refuses unknown fields, so a typo
// like "sleep_qualty" is an error instead of being silently ignored.
func decodeStrict(body []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("body is not valid JSON for this endpoint: %v", err)
	}
	return nil
}

// badRequest marks an error caused by what the client sent; it is
// answered with 400 and its message.
type badRequest struct{ err error }

func (e badRequest) Error() string { return e.err.Error() }
func (e badRequest) Unwrap() error { return e.err }

// failJournal answers 400 for a bad request and 500 for anything else.
func (s *Server) failJournal(w http.ResponseWriter, err error) {
	var bad badRequest
	if errors.As(err, &bad) {
		writeJSON(w, http.StatusBadRequest, api.ErrorResponse{Error: err.Error()})
		return
	}
	s.fail(w, err)
}
