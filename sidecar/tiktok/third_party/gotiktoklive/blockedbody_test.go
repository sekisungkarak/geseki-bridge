package gotiktoklive

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"go.uber.org/ratelimit"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/proto"
)

// TestGetRoomDataEmptyBodyIsBlocked: TikTok menjawab /webcast/im/fetch dengan
// 200 dan body KOSONG saat sesi/fingerprint ditandai (batas per-IP). Itu cara
// TikTok menyatakan penolakan tanpa status error. Sebelum pemeriksaan ini,
// penolakan mengalir sebagai "drop biasa": sidecar memakai ladder pendek dan
// menembak tiap 30 detik sehingga batasnya tidak pernah sembuh. Balasan sah
// selalu protobuf puluhan KB, jadi panjang nol tidak ambigu.
func TestGetRoomDataEmptyBodyIsBlocked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 200 dengan body kosong: bentuk penolakan yang harus dikenali.
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	live := newTestLive(t, srv.URL)

	err := live.getRoomData()
	if err == nil {
		t.Fatal("getRoomData() = nil; body kosong harus dianggap penolakan")
	}
	if _, ok := err.(*ErrIPBlockedOrBanned); !ok {
		t.Fatalf("getRoomData() error = %T (%v); want *ErrIPBlockedOrBanned", err, err)
	}
}

// TestGetRoomDataWithoutPushServerIsBlocked: balasan yang diterima TAPI tanpa
// PushServer membuat l.wsURL kosong, dan siklusnya berhenti pada "cannot
// upgrade connection without a wsURL". Gejalanya sama dengan penolakan, jadi
// harus diklasifikasikan sama — kalau tidak, ladder panjang tidak pernah aktif.
func TestGetRoomDataWithoutPushServerIsBlocked(t *testing.T) {
	// Protobuf sah tapi tanpa pushServer (field 10) dan tanpa routeParamsMap.
	body, err := proto.Marshal(&pb.WebcastResponse{Cursor: "c1"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Header cookie wajib ada: getRoomData menolak balasan tanpanya sebelum
		// sampai ke pemeriksaan PushServer.
		w.Header().Set("X-Set-TT-Cookie", "sessionid=abc; tt-target-idc=useast")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	live := newTestLive(t, srv.URL)

	err = live.getRoomData()
	if err == nil {
		t.Fatal("getRoomData() = nil; balasan tanpa PushServer harus dianggap penolakan")
	}
	if _, ok := err.(*ErrIPBlockedOrBanned); !ok {
		t.Fatalf("getRoomData() error = %T (%v); want *ErrIPBlockedOrBanned", err, err)
	}
}

// TestGetRoomDataWithPushServerSucceeds: jalur sehat tidak boleh ikut rusak.
// Balasan dengan PushServer + routeParamsMap harus mengisi wsURL dan lolos.
func TestGetRoomDataWithPushServerSucceeds(t *testing.T) {
	body, err := proto.Marshal(&pb.WebcastResponse{
		Cursor:         "c2",
		PushServer:     "wss://push.example/ws",
		RouteParamsMap: map[string]string{"k": "v"},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Set-TT-Cookie", "sessionid=abc; tt-target-idc=useast")
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	live := newTestLive(t, srv.URL)

	if err := live.getRoomData(); err != nil {
		t.Fatalf("getRoomData() = %v; jalur sehat harus lolos", err)
	}
	if live.wsURL != "wss://push.example/ws" {
		t.Errorf("wsURL = %q, want wss://push.example/ws", live.wsURL)
	}
}

// newTestLive membangun Live yang menembak server uji, bukan TikTok. The HTTP
// client keeps no cookies and never follows redirects, and wsParams is set so
// a healthy reply is not rejected for a different reason.
func newTestLive(t *testing.T, base string) *Live {
	t.Helper()
	u, err := url.Parse(base)
	if err != nil {
		t.Fatalf("parse %q: %v", base, err)
	}
	jar, _ := cookiejar.New(nil)
	tt := &TikTok{
		c:          &http.Client{Jar: jar},
		mu:         &sync.Mutex{},
		signerUrl:  base,
		clientName: clientNameDefault,
		limiter:    ratelimit.New(100, ratelimit.Per(1*time.Minute), ratelimit.WithoutSlack),
		infoHandler:  func(...interface{}) {},
		warnHandler:  func(...interface{}) {},
		debugHandler: func(...interface{}) {},
		errHandler:   func(...interface{}) {},
	}
	_ = u
	return &Live{
		t:        tt,
		ID:       "7692858847816911636",
		Events:   make(chan Event, 8),
		chanSize: DEFAULT_EVENTS_CHAN_SIZE,
	}
}
