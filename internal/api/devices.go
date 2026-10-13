package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const revokedSelfWarning = "this device was revoked; you are now signed out"

func (s *server) handleDevices(w http.ResponseWriter, r *http.Request) {
	devs, err := s.st.Devices(r.Context())
	if err != nil {
		slog.Error("api: list devices failed", "err", err)
		writeAPIErr(w, newErr(http.StatusInternalServerError, "internal", "listing devices failed"))
		return
	}
	if devs == nil {
		devs = []store.Device{}
	}
	writeJSON(w, http.StatusOK, devs)
}

func (s *server) handleRevokeDevice(w http.ResponseWriter, r *http.Request) {
	dev := deviceFrom(r)
	if ok, wait := s.actionLimit.allow(dev.ID); !ok {
		writeTooMany(w, wait)
		return
	}
	id := r.PathValue("id")
	payload, _ := json.Marshal(map[string]string{"id": id})

	var aerr *apiErr
	if err := s.st.RevokeDevice(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			aerr = newErr(http.StatusNotFound, "not_found", "no such device")
		} else {
			slog.Error("api: revoke device failed", "device", id, "err", err)
			aerr = newErr(http.StatusInternalServerError, "internal", "revoking device failed")
		}
		aerr.cause = err
	} else {
		s.auth.forget(id)
	}
	s.auditRevoke(r, dev, string(payload), aerr)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}

	resp := map[string]any{"ok": true}
	if id == dev.ID {
		resp["warning"] = revokedSelfWarning
		http.SetCookie(w, &http.Cookie{
			Name:     cookieName,
			Value:    "",
			Path:     "/",
			MaxAge:   -1,
			HttpOnly: true,
			Secure:   s.secureCookie(r),
			SameSite: http.SameSiteStrictMode,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

// auditRevoke records the attempt after it finished, so failures are logged too.
func (s *server) auditRevoke(r *http.Request, dev store.Device, payload string, aerr *apiErr) {
	result := "ok"
	if aerr != nil {
		result = aerr.Error()
	}
	// Detached from the request so a client that hangs up cannot lose the row.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	err := s.st.AddAudit(ctx, store.AuditEntry{
		At:      s.now(),
		Actor:   dev.ID,
		Surface: "api",
		Action:  "revoke_device",
		Payload: payload,
		Result:  result,
	})
	if err != nil {
		slog.Error("api: audit write failed", "action", "revoke_device", "err", err)
	}
}
