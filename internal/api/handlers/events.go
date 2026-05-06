package handlers

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/smalex-z/gopher/internal/api/response"
	"github.com/smalex-z/gopher/internal/db"
)

// EventsHandler exposes the unified events table for the /logs page.
type EventsHandler struct{}

func NewEventsHandler() *EventsHandler { return &EventsHandler{} }

// GET /api/events
//
// Query params (all optional):
//
//	source       comma-separated list: auth,machine,tunnel,health,firewall,system
//	severity     exact match: info | warn | error | critical
//	min_severity threshold: info | warn | error | critical
//	resource_id  filter to events for one specific resource
//	q            substring match on message / resource_name / kind
//	since        RFC3339 timestamp lower bound
//	until        RFC3339 timestamp upper bound
//	before       RFC3339 cursor for pagination — pass the oldest CreatedAt of
//	             the last page to fetch older rows
//	limit        page size (default 100, max 500)
//
// Response shape:
//
//	{ "data": { "events": [...], "total": 1234 } }
//
// Total reflects the filter ignoring pagination, so the UI can show
// "showing 100 of 1234".
func (h *EventsHandler) List(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := db.EventFilter{
		Severity:    q.Get("severity"),
		MinSeverity: q.Get("min_severity"),
		ResourceID:  q.Get("resource_id"),
		Search:      strings.TrimSpace(q.Get("q")),
	}

	if raw := q.Get("source"); raw != "" {
		// Comma-separated list. Trim each and drop empties.
		parts := strings.Split(raw, ",")
		out := make([]string, 0, len(parts))
		for _, p := range parts {
			if v := strings.TrimSpace(p); v != "" {
				out = append(out, v)
			}
		}
		filter.Sources = out
	}

	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(w, "invalid since: must be RFC3339")
			return
		}
		filter.Since = t
	}
	if raw := q.Get("until"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(w, "invalid until: must be RFC3339")
			return
		}
		filter.Until = t
	}
	if raw := q.Get("before"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			response.BadRequest(w, "invalid before: must be RFC3339")
			return
		}
		filter.Before = t
	}

	limit := 100
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			response.BadRequest(w, "invalid limit")
			return
		}
		if n > 500 {
			n = 500
		}
		limit = n
	}
	filter.Limit = limit

	events, err := db.GetEvents(filter)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}
	total, err := db.CountEvents(filter)
	if err != nil {
		response.InternalError(w, err.Error())
		return
	}

	response.Success(w, map[string]interface{}{
		"events": events,
		"total":  total,
	})
}
