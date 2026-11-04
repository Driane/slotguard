package webhook

import (
	"errors"
	"testing"
	"time"
)

func TestVerify(t *testing.T) {
	secret := []byte("s3cret")
	body := []byte(`{"id":1,"type":"booking.created"}`)
	sentAt := time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC)
	header := Sign(secret, sentAt, body)

	tests := []struct {
		name    string
		secret  []byte
		header  string
		body    []byte
		now     time.Time
		wantErr error
	}{
		{name: "valid signature", secret: secret, header: header, body: body, now: sentAt.Add(time.Minute)},
		{name: "altered body", secret: secret, header: header, body: []byte(`{"id":2}`), now: sentAt, wantErr: ErrBadSignature},
		{name: "wrong secret", secret: []byte("other"), header: header, body: body, now: sentAt, wantErr: ErrBadSignature},
		{name: "replayed too late", secret: secret, header: header, body: body, now: sentAt.Add(10 * time.Minute), wantErr: ErrStaleSignature},
		{name: "empty header", secret: secret, header: "", body: body, now: sentAt, wantErr: ErrMalformedSignature},
		{name: "garbage header", secret: secret, header: "t=yesterday,v1=zz", body: body, now: sentAt, wantErr: ErrMalformedSignature},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := Verify(tt.secret, tt.header, tt.body, tt.now, 5*time.Minute)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
