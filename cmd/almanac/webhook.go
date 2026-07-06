package main

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/mutouyun/almanac/internal/store"
)

// webhookHeader is the header carrying the per-user webhook token.
const webhookHeader = "X-Webhook-Token"

// webhookRequest is the payload for POST /api/webhook/entry.
//
// Amount is deliberately json.Number (not float64) so we can convert the
// decimal string to integer cents without IEEE-754 truncation.
type webhookRequest struct {
	Date   string      `json:"date"`   // RFC3339 with tz offset
	Type   string      `json:"type"`   // raw description
	Amount json.Number `json:"amount"` // yuan, unsigned magnitude (legacy sign ignored)
}

// webhookResponse is returned on successful ingestion.
type webhookResponse struct {
	Status      string `json:"status"`       // "ok"
	ID          int64  `json:"id"`           // new entry id
	AmountCents int64  `json:"amount_cents"` // stored unsigned cents (absolute value)
	RecordTime  string `json:"record_time"`  // normalized CST wall time
	Classified  bool   `json:"classified"`   // false = unclassified (MVP: always false)
}

func webhookHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")

		// Authenticate by webhook token header.
		token := r.Header.Get(webhookHeader)
		if token == "" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "missing webhook token"})
			return
		}
		u, err := st.UserByWebhookToken(token)
		if err != nil {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid webhook token"})
			return
		}

		// Decode with UseNumber so amount stays a string-backed json.Number.
		dec := json.NewDecoder(r.Body)
		dec.UseNumber()
		var req webhookRequest
		if err := dec.Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid request body"})
			return
		}
		if req.Type == "" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "type is required"})
			return
		}

		cents, err := parseAmountToCents(req.Amount.String())
		if err == errZeroAmount {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "amount must not be zero"})
			return
		}
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid amount"})
			return
		}

		recordTime, err := normalizeRecordTime(req.Date)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "invalid date"})
			return
		}

		ledgerID, err := st.DefaultLedgerID(u.ID)
		if err != nil {
			log.Printf("webhook: no default ledger for user %d: %v", u.ID, err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}

		// Route to a category if any rule matches.
		categoryID := st.RouteEntry(u.ID, cents, req.Type)

		id, err := st.InsertEntry(store.Entry{
			UserID:      u.ID,
			LedgerID:    ledgerID,
			CategoryID:  categoryID,
			AmountCents: cents,
			RawType:     req.Type,
			RecordTime:  recordTime,
			Source:      "webhook",
		})
		if err != nil {
			log.Printf("webhook: insert entry failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(errorResponse{Error: "internal error"})
			return
		}

		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(webhookResponse{
			Status:      "ok",
			ID:          id,
			AmountCents: cents,
			RecordTime:  recordTime,
			Classified:  categoryID != nil,
		})
	}
}
