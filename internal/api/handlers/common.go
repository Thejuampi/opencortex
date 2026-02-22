package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"opencortex/internal/service"
)

type pagination struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
	Total   int `json:"total"`
}

type envelope struct {
	OK         bool        `json:"ok"`
	Data       any         `json:"data"`
	Error      any         `json:"error"`
	Pagination *pagination `json:"pagination"`
}

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, data any, pg *pagination) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{
		OK:         status >= 200 && status < 300,
		Data:       data,
		Error:      nil,
		Pagination: pg,
	})
}

func writeErr(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{
		OK:    false,
		Data:  nil,
		Error: apiError{Code: code, Message: message},
	})
}

func WriteErrPublic(w http.ResponseWriter, status int, code, message string) {
	writeErr(w, status, code, message)
}

func decodeJSON(r *http.Request, dst any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func parseInt(value string, defaultValue int) int {
	v, err := strconv.Atoi(value)
	if err != nil || v <= 0 {
		return defaultValue
	}
	return v
}

func mapServiceErr(w http.ResponseWriter, err error) bool {
	switch {
	case errors.Is(err, service.ErrUnauthorized):
		writeErr(w, http.StatusUnauthorized, "UNAUTHORIZED", "unauthorized")
		return true
	case errors.Is(err, service.ErrForbidden):
		writeErr(w, http.StatusForbidden, "FORBIDDEN", "forbidden")
		return true
	case errors.Is(err, service.ErrNotFound):
		writeErr(w, http.StatusNotFound, "NOT_FOUND", "not found")
		return true
	case errors.Is(err, service.ErrValidation):
		writeErr(w, http.StatusBadRequest, "VALIDATION_ERROR", err.Error())
		return true
	case errors.Is(err, service.ErrConflict):
		writeErr(w, http.StatusConflict, "CONFLICT", err.Error())
		return true
	default:
		return false
	}
}
