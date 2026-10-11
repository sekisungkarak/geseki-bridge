package gotiktoklive

// Jalur "direct": terhubung ke WebSocket TikTok TANPA sign server, tanpa API
// key, dan tanpa X-Bogus/msToken. Ini menghapus ketergantungan pada layanan
// penandatangan pihak ketiga (beserta kuota per-IP-nya) yang selama ini
// menjadi penyebab koneksi gagal.
//
// Alurnya, semua ke TikTok langsung:
//  1. Ambil cookie ttwid anonim dari https://www.tiktok.com/.
//  2. Buka wss://webcast-ws.tiktok.com/webcast/im/ws_proxy/ws_reuse_supplement/
//     dengan parameter ws_direct=1.
//  3. Kirim frame heartbeat dan enter-room, lalu baca frame protobuf seperti
//     biasa lewat readSocket/parseWssMsg.
//
// Parameter ws_direct=1 memilih endpoint yang tidak ditandatangani, jadi
// TikTok tidak meminta X-Bogus/msToken dan tidak ada kuota per-IP.

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"golang.org/x/net/proxy"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	pb "github.com/steampoweredtaco/gotiktoklive/proto"
)

// fetchTTWID mengambil cookie ttwid anonim. TikTok hanya mengirim cookie ini
// pada sebagian kecil permintaan anonim, jadi percobaan diulang beberapa kali.
// Cookie ini bukan kredensial: nilainya tidak terikat akun dan aman dipakai
// ulang antar percobaan koneksi.
func (t *TikTok) fetchTTWID(ua string) (string, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if t.proxy != nil {
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = http.ProxyURL(t.proxy)
		client.Transport = tr
	}

	const attempts = 8
	for i := 0; i < attempts; i++ {
		req, err := http.NewRequest("GET", tiktokBaseUrl, nil)
		if err != nil {
			return "", err
		}
		req.Header.Set("User-Agent", ua)
		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		var found string
		for _, c := range resp.Cookies() {
			if c.Name == "ttwid" {
				found = c.Value
				break
			}
		}
		resp.Body.Close()
		if found != "" {
			return found, nil
		}
		time.Sleep(750 * time.Millisecond)
	}
	return "", fmt.Errorf("no ttwid cookie after %d attempts", attempts)
}

// prepareDirect menyiapkan koneksi direct untuk room ini: mengambil ttwid lalu
// menyusun URL WebSocket. Dipanggil dari fetchRoom menggantikan getRoomData
// (jalur /webcast/fetch bertanda tangan).
func (l *Live) prepareDirect() error {
	ua := directUserAgent
	ttwid, err := l.t.fetchTTWID(ua)
	if err != nil {
		return fmt.Errorf("direct: ttwid: %w", err)
	}
	l.ttwid = ttwid
	l.directUA = ua
	l.wsURL = buildDirectWSURL(directCDNHost, l.ID)
	return nil
}

// buildDirectWSURL menyusun URL WebSocket endpoint ws_direct. Parameternya
// disalin dari klien web TikTok: tanpa ini TikTok menutup handshake.
func buildDirectWSURL(cdnHost, roomID string) string {
	lastRtt := 100.0 + rand.Float64()*100.0
	params := url.Values{
		"version_code":          {"180800"},
		"device_platform":       {"web"},
		"cookie_enabled":        {"true"},
		"screen_width":          {"1920"},
		"screen_height":         {"1080"},
		"browser_language":      {"en-US"},
		"browser_platform":      {"Linux x86_64"},
		"browser_name":          {"Mozilla"},
		"browser_version":       {"5.0 (X11)"},
		"browser_online":        {"true"},
		"tz_name":               {"UTC"},
		"app_name":              {"tiktok_web"},
		"sup_ws_ds_opt":         {"1"},
		"update_version_code":   {"2.0.0"},
		"compress":              {""},
		"webcast_language":      {"en"},
		"ws_direct":             {"1"},
		"aid":                   {"1988"},
		"live_id":               {"12"},
		"app_language":          {"en"},
		"client_enter":          {"1"},
		"room_id":               {roomID},
		"identity":              {"audience"},
		"history_comment_count": {"6"},
		"last_rtt":              {fmt.Sprintf("%.3f", lastRtt)},
		"heartbeat_duration":    {strconv.FormatInt((10 * time.Second).Milliseconds(), 10)},
		"resp_content_type":     {"protobuf"},
		"did_rule":              {"3"},
	}
	return fmt.Sprintf("wss://%s%s?%s", cdnHost, directWSPath, params.Encode())
}

// connectDirect membuka WebSocket direct dan mengirim frame pembuka
// (heartbeat lalu enter-room). Setelah ini readSocket memakai jalur baca yang
// sama dengan koneksi biasa.
func (l *Live) connectDirect() error {
	headers := http.Header{}
	headers.Set("User-Agent", l.directUA)
	headers.Set("Cookie", "ttwid="+l.ttwid)
	headers.Set("Origin", origin)
	headers.Set("Referer", referer)
	headers.Set("Accept-Language", "en-US,en;q=0.9")
	headers.Set("Accept-Encoding", "gzip, deflate")
	headers.Set("Cache-Control", "no-cache")

	proxyNetDial := proxy.FromEnvironment()
	if l.t.proxy != nil {
		d, err := proxy.FromURL(l.t.proxy, proxy.Direct)
		if err != nil {
			return fmt.Errorf("direct: configure proxy dialer: %w", err)
		}
		proxyNetDial = d
	} else if hp := os.Getenv("HTTPS_PROXY"); hp != "" {
		if uri, err := url.Parse(hp); err == nil {
			if d, err := proxy.FromURL(uri, proxy.Direct); err == nil {
				proxyNetDial = d
			}
		}
	}

	dialer := ws.Dialer{
		Header: ws.HandshakeHeaderHTTP(headers),
		NetDial: func(ctx context.Context, a, b string) (net.Conn, error) {
			return proxyNetDial.Dial(a, b)
		},
	}

	conn, _, _, err := dialer.Dial(context.Background(), l.wsURL)
	if err != nil {
		return fmt.Errorf("direct: dial: %w", err)
	}

	hb, err := buildHeartbeatFrame(l.ID)
	if err != nil {
		conn.Close()
		return err
	}
	if err := wsutil.WriteClientBinary(conn, hb); err != nil {
		conn.Close()
		return fmt.Errorf("direct: send heartbeat: %w", err)
	}

	enter, err := buildEnterRoomFrame(l.ID)
	if err != nil {
		conn.Close()
		return err
	}
	if err := wsutil.WriteClientBinary(conn, enter); err != nil {
		conn.Close()
		return fmt.Errorf("direct: send enter room: %w", err)
	}

	l.wss = conn
	return nil
}

// sendHeartbeatDirect mengirim frame heartbeat secara berkala. Ini pengganti
// sendPing pada koneksi direct: endpoint ws_direct mengharapkan
// HeartbeatMessage, bukan frame "hb" kosong.
func (l *Live) sendHeartbeatDirect() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-l.done():
			return
		case <-l.t.done():
			return
		case <-ticker.C:
			hb, err := buildHeartbeatFrame(l.ID)
			if err != nil {
				l.t.errHandler(err)
				return
			}
			if err := wsutil.WriteClientBinary(l.wss, hb); err != nil {
				l.t.errHandler(fmt.Errorf("direct heartbeat: %w", err))
				return
			}
		}
	}
}

// buildHeartbeatFrame menyusun WebcastPushFrame{PayloadType:"hb"} yang memuat
// HeartbeatMessage{room_id}. HeartbeatMessage tidak ada di proto vendored, jadi
// field-nya ditulis tangan lewat protowire (field 1 = room_id, varint).
func buildHeartbeatFrame(roomID string) ([]byte, error) {
	rid, err := strconv.ParseUint(roomID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("heartbeat: invalid room_id %q: %w", roomID, err)
	}
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.VarintType)
	inner = protowire.AppendVarint(inner, rid)

	frame := &pb.WebcastPushFrame{
		PayloadEncoding: "pb",
		PayloadType:     "hb",
		Payload:         inner,
	}
	return proto.Marshal(frame)
}

// buildEnterRoomFrame menyusun WebcastPushFrame{PayloadType:"im_enter_room"}
// yang memuat WebcastImEnterRoomMessage. Sama seperti heartbeat, message ini
// tidak ada di proto vendored sehingga field-nya ditulis tangan lewat
// protowire: 1=room_id(varint), 4=live_id(varint), 5=identity(string),
// 9=filter_welcome_msg(string).
func buildEnterRoomFrame(roomID string) ([]byte, error) {
	rid, err := strconv.ParseInt(roomID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("enter room: invalid room_id %q: %w", roomID, err)
	}
	var inner []byte
	inner = protowire.AppendTag(inner, 1, protowire.VarintType)
	inner = protowire.AppendVarint(inner, uint64(rid))
	inner = protowire.AppendTag(inner, 4, protowire.VarintType)
	inner = protowire.AppendVarint(inner, 12)
	inner = protowire.AppendTag(inner, 5, protowire.BytesType)
	inner = protowire.AppendString(inner, "audience")
	inner = protowire.AppendTag(inner, 9, protowire.BytesType)
	inner = protowire.AppendString(inner, "0")

	frame := &pb.WebcastPushFrame{
		PayloadEncoding: "pb",
		PayloadType:     "im_enter_room",
		Payload:         inner,
	}
	return proto.Marshal(frame)
}

// gunzipIfNeeded membuka payload bila berawalan magic gzip. Endpoint direct
// dapat mengirim payload terkompresi; proto WebcastResponse sendiri tidak
// terkompresi, jadi tanpa ini unmarshal akan gagal. Pemeriksaan magic-byte
// membuatnya aman untuk payload yang sudah tidak terkompresi.
func gunzipIfNeeded(data []byte) ([]byte, error) {
	if len(data) < 2 || data[0] != 0x1f || data[1] != 0x8b {
		return data, nil
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return nil, err
	}
	return out, nil
}
