package gotiktoklive

import (
	"bytes"
	"compress/gzip"
	"net/url"
	"strings"
	"testing"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
)

// TestBuildDirectWSURL memastikan URL endpoint direct terbentuk dengan
// parameter kunci yang diminta TikTok. ws_direct=1 adalah inti jalur ini:
// tanpanya endpoint ws_reuse_supplement tidak menerima koneksi tanpa tanda
// tangan. room_id harus hadir agar TikTok tahu room mana yang dimasuki.
func TestBuildDirectWSURL(t *testing.T) {
	got := buildDirectWSURL("webcast-ws.tiktok.com", "7694909509579344646")

	if !strings.HasPrefix(got, "wss://webcast-ws.tiktok.com/webcast/im/ws_proxy/ws_reuse_supplement/?") {
		t.Fatalf("base URL salah: %s", got)
	}
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("URL tidak bisa diparse: %v", err)
	}
	q := u.Query()
	checks := map[string]string{
		"ws_direct":    "1",
		"room_id":      "7694909509579344646",
		"identity":     "audience",
		"aid":          "1988",
		"live_id":      "12",
		"client_enter": "1",
	}
	for k, want := range checks {
		if q.Get(k) != want {
			t.Errorf("param %s = %q, mau %q", k, q.Get(k), want)
		}
	}
}

// TestBuildHeartbeatFrame memastikan frame heartbeat berbentuk
// WebcastPushFrame{PayloadType:"hb"} yang memuat HeartbeatMessage{room_id}
// (field 1, varint). HeartbeatMessage tidak ada di proto vendored, jadi field
// ditulis tangan lewat protowire; test ini mengunci nomor field itu.
func TestBuildHeartbeatFrame(t *testing.T) {
	b, err := buildHeartbeatFrame("7694909509579344646")
	if err != nil {
		t.Fatalf("buildHeartbeatFrame: %v", err)
	}
	var frame pb.WebcastPushFrame
	if err := proto.Unmarshal(b, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if frame.PayloadType != "hb" {
		t.Errorf("PayloadType = %q, mau \"hb\"", frame.PayloadType)
	}
	field, typ, n := protowire.ConsumeTag(frame.Payload)
	if n < 0 || field != 1 || typ != protowire.VarintType {
		t.Fatalf("tag heartbeat salah: field=%d type=%v", field, typ)
	}
	rid, n := protowire.ConsumeVarint(frame.Payload[n:])
	if n < 0 {
		t.Fatalf("room_id bukan varint")
	}
	if rid != 7694909509579344646 {
		t.Errorf("room_id = %d, mau 7694909509579344646", rid)
	}
}

// TestBuildEnterRoomFrame memastikan frame enter-room berbentuk
// WebcastPushFrame{PayloadType:"im_enter_room"} dengan field yang benar:
// 1=room_id(varint), 4=live_id(varint), 5=identity(string),
// 9=filter_welcome_msg(string).
func TestBuildEnterRoomFrame(t *testing.T) {
	b, err := buildEnterRoomFrame("7694909509579344646")
	if err != nil {
		t.Fatalf("buildEnterRoomFrame: %v", err)
	}
	var frame pb.WebcastPushFrame
	if err := proto.Unmarshal(b, &frame); err != nil {
		t.Fatalf("unmarshal frame: %v", err)
	}
	if frame.PayloadType != "im_enter_room" {
		t.Errorf("PayloadType = %q, mau \"im_enter_room\"", frame.PayloadType)
	}
	got := map[protowire.Number]interface{}{}
	p := frame.Payload
	for len(p) > 0 {
		num, typ, n := protowire.ConsumeTag(p)
		if n < 0 {
			t.Fatalf("tag rusak")
		}
		p = p[n:]
		switch typ {
		case protowire.VarintType:
			v, n := protowire.ConsumeVarint(p)
			if n < 0 {
				t.Fatalf("varint rusak")
			}
			got[num] = v
			p = p[n:]
		case protowire.BytesType:
			v, n := protowire.ConsumeBytes(p)
			if n < 0 {
				t.Fatalf("bytes rusak")
			}
			got[num] = string(v)
			p = p[n:]
		default:
			t.Fatalf("tipe tak terduga %v", typ)
		}
	}
	if got[1] != uint64(7694909509579344646) {
		t.Errorf("field1 room_id = %v", got[1])
	}
	if got[4] != uint64(12) {
		t.Errorf("field4 live_id = %v", got[4])
	}
	if got[5] != "audience" {
		t.Errorf("field5 identity = %v", got[5])
	}
	if got[9] != "0" {
		t.Errorf("field9 filter_welcome_msg = %v", got[9])
	}
}

// TestGunzipIfNeeded: endpoint direct bisa mengirim payload terkompresi.
// Fungsi harus membuka data bergzip dan membiarkan data polos apa adanya,
// supaya jalur lama (yang tidak pernah bergzip) tetap aman.
func TestGunzipIfNeeded(t *testing.T) {
	plain := []byte("hello-tiktok-protobuf")
	out, err := gunzipIfNeeded(plain)
	if err != nil {
		t.Fatalf("plain: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Errorf("plain berubah: %q", out)
	}

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(plain); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	zw.Close()
	out, err = gunzipIfNeeded(buf.Bytes())
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	if !bytes.Equal(out, plain) {
		t.Errorf("gzip hasil = %q, mau %q", out, plain)
	}
}
