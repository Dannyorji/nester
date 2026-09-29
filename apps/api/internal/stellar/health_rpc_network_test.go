package stellar

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFetchRPCNetworkPassphrase(t *testing.T) {
	cases := []struct {
		name, body, want, wantErr string
	}{
		{
			name: "returns passphrase",
			body: `{"jsonrpc":"2.0","id":"x","result":{"passphrase":"Test SDF Network ; September 2015","protocolVersion":21}}`,
			want: "Test SDF Network ; September 2015",
		},
		{
			name:    "rpc error",
			body:    `{"jsonrpc":"2.0","id":"x","error":{"code":-32601,"message":"method not found"}}`,
			wantErr: "method not found",
		},
		{
			name:    "empty passphrase",
			body:    `{"jsonrpc":"2.0","id":"x","result":{}}`,
			wantErr: "no passphrase",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				if !strings.Contains(string(raw), `"getNetwork"`) {
					t.Errorf("expected getNetwork request, got %s", raw)
				}
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := FetchRPCNetworkPassphrase(context.Background(), srv.Client(), srv.URL)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got (%q, %v), want %q", got, err, tc.want)
			}
		})
	}
}

func TestPingHorizonReportsNetworkPassphrase(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"history_latest_ledger":42,"network_passphrase":"Public Global Stellar Network ; September 2015"}`))
	}))
	defer srv.Close()

	res := PingHorizon(context.Background(), srv.Client(), srv.URL)
	if !res.OK || res.NetworkPassphrase != "Public Global Stellar Network ; September 2015" {
		t.Fatalf("unexpected result %+v", res)
	}
}
