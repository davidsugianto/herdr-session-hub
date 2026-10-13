package notify

import (
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/SherClockHolmes/webpush-go"

	"github.com/davidsugianto/herdr-session-hub/internal/store"
)

const (
	pushTTL           = 60 // seconds the push service keeps an undelivered message
	pushClientTimeout = 10 * time.Second
)

// webPushSink sends payloads with VAPID-signed, aes128gcm-encrypted Web Push.
type webPushSink struct {
	public, private string
	subscriber      string
	client          webpush.HTTPClient
}

// newWebPushSink returns a sink; a nil client uses newPushClient.
func newWebPushSink(public, private, contact string, client webpush.HTTPClient) *webPushSink {
	if client == nil {
		client = newPushClient()
	}
	return &webPushSink{public: public, private: private, subscriber: subscriber(contact), client: client}
}

// newPushClient never follows redirects, so a signed push request cannot be
// bounced to another host; a 3xx is reported as a failed send.
func newPushClient() *http.Client {
	return &http.Client{
		Timeout: pushClientTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// Send implements Sink. 404/410 map to ErrGone.
func (w *webPushSink) Send(ctx context.Context, sub store.PushSub, p Payload) error {
	msg, err := json.Marshal(p)
	if err != nil {
		return fmt.Errorf("notify: encode payload: %w", err)
	}
	urgency := webpush.UrgencyNormal
	if p.Kind == KindBlocked {
		urgency = webpush.UrgencyHigh
	}
	resp, err := webpush.SendNotificationWithContext(ctx, msg, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      w.client,
		Subscriber:      w.subscriber,
		TTL:             pushTTL,
		Urgency:         urgency,
		VAPIDPublicKey:  w.public,
		VAPIDPrivateKey: w.private,
	})
	if err != nil {
		return fmt.Errorf("notify: web push: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return ErrGone
	}
	return fmt.Errorf("notify: push service returned %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// generateVAPID makes a P-256 key pair encoded like webpush-go expects:
// base64url (no padding) of the uncompressed public point and the raw scalar.
func generateVAPID() (public, private string, err error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", "", err
	}
	return base64.RawURLEncoding.EncodeToString(k.PublicKey().Bytes()),
		base64.RawURLEncoding.EncodeToString(k.Bytes()), nil
}

// checkVAPID verifies a stored pair decodes and that the public key belongs to
// the private one.
func checkVAPID(public, private string) error {
	pb, err1 := decodeKey(public)
	kb, err2 := decodeKey(private)
	if err1 != nil || err2 != nil {
		return fmt.Errorf("notify: stored VAPID keys are not base64url")
	}
	k, err := ecdh.P256().NewPrivateKey(kb)
	if err != nil {
		return fmt.Errorf("notify: stored VAPID private key: %w", err)
	}
	if string(k.PublicKey().Bytes()) != string(pb) {
		return fmt.Errorf("notify: stored VAPID public key does not match the private key")
	}
	return nil
}

func decodeKey(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}
