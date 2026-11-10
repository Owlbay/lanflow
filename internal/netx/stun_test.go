package netx

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"
)

// fakeSTUN is a local UDP server answering Binding Requests. reply builds
// the response body (attributes) for the request's transaction id; a nil
// reply swallows the request (a blocked / dead server).
func fakeSTUN(t *testing.T, reply func(txid [12]byte) (typ uint16, body []byte)) string {
	t.Helper()
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n < stunHeaderLen || binary.BigEndian.Uint16(buf[0:]) != stunBindingRequest || reply == nil {
				continue
			}
			var txid [12]byte
			copy(txid[:], buf[8:20])
			typ, body := reply(txid)
			msg := make([]byte, stunHeaderLen+len(body))
			binary.BigEndian.PutUint16(msg[0:], typ)
			binary.BigEndian.PutUint16(msg[2:], uint16(len(body)))
			binary.BigEndian.PutUint32(msg[4:], stunMagicCookie)
			copy(msg[8:], txid[:])
			copy(msg[20:], body)
			_, _ = pc.WriteTo(msg, from)
		}
	}()
	return pc.LocalAddr().String()
}

func attr(typ uint16, val []byte) []byte {
	out := make([]byte, 4+len(val)+pad4(len(val)))
	binary.BigEndian.PutUint16(out[0:], typ)
	binary.BigEndian.PutUint16(out[2:], uint16(len(val)))
	copy(out[4:], val)
	return out
}

// A server that answers with XOR-MAPPED-ADDRESS gives the public address;
// one with only MAPPED-ADDRESS (old servers) also works.
func TestSTUNPublicIP(t *testing.T) {
	want := netip.MustParseAddrPort("203.0.113.9:40000")
	xorSrv := fakeSTUN(t, func(txid [12]byte) (uint16, []byte) {
		body := attr(0x8022, []byte("fake")) // SOFTWARE, before the address
		return stunBindingResponse, append(body, attr(attrXORMappedAddr, EncodeSTUNAddress(want, true, txid))...)
	})
	plainSrv := fakeSTUN(t, func(txid [12]byte) (uint16, []byte) {
		return stunBindingResponse, attr(attrMappedAddress, EncodeSTUNAddress(want, false, txid))
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, sv := range []string{xorSrv, plainSrv} {
		got, err := STUNPublicIP(ctx, []string{sv}, nil)
		if err != nil || got != want.Addr() {
			t.Fatalf("%s: got %v, %v", sv, got, err)
		}
	}
}

// A dead first server is skipped and the second one answers; when none
// answers, the error is ErrSTUNUnreachable (UDP blocked).
func TestSTUNFallbackAndUnreachable(t *testing.T) {
	dead := fakeSTUN(t, nil)
	want := netip.MustParseAddrPort("[2001:db8::9]:5000")
	live := fakeSTUN(t, func(txid [12]byte) (uint16, []byte) {
		return stunBindingResponse, attr(attrXORMappedAddr, EncodeSTUNAddress(want, true, txid))
	})
	// Shorten the per-server wait through a dialer that sets its own deadline.
	quick := func(ctx context.Context, network, addr string) (net.Conn, error) {
		c, err := (&net.Dialer{}).DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return deadlineConn{c, 300 * time.Millisecond}, nil
	}
	got, err := STUNPublicIP(context.Background(), []string{dead, live}, quick)
	if err != nil || got != want.Addr() {
		t.Fatalf("fallback: got %v, %v", got, err)
	}
	_, err = STUNPublicIP(context.Background(), []string{dead}, quick)
	if !errors.Is(err, ErrSTUNUnreachable) {
		t.Fatalf("dead server: err = %v", err)
	}
	errSrv := fakeSTUN(t, func([12]byte) (uint16, []byte) {
		return stunBindingError, attr(attrErrorCode, []byte{0, 0, 4, 20, 'U', 'n', 'k'})
	})
	if _, err := STUNPublicIP(context.Background(), []string{errSrv}, quick); err == nil || !errors.Is(err, ErrSTUNUnreachable) {
		t.Fatalf("error response: err = %v", err)
	}
}

// deadlineConn overrides the deadline stunBinding sets with a shorter one.
type deadlineConn struct {
	net.Conn
	d time.Duration
}

func (c deadlineConn) SetDeadline(time.Time) error { return c.Conn.SetDeadline(time.Now().Add(c.d)) }

// The parser rejects garbage, other transactions and truncated attributes.
func TestParseSTUNResponse(t *testing.T) {
	txid := [12]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	ap := netip.MustParseAddrPort("198.51.100.4:3478")
	mk := func(typ uint16, body []byte, id [12]byte) []byte {
		msg := make([]byte, stunHeaderLen+len(body))
		binary.BigEndian.PutUint16(msg[0:], typ)
		binary.BigEndian.PutUint16(msg[2:], uint16(len(body)))
		binary.BigEndian.PutUint32(msg[4:], stunMagicCookie)
		copy(msg[8:], id[:])
		copy(msg[20:], body)
		return msg
	}
	good := mk(stunBindingResponse, attr(attrXORMappedAddr, EncodeSTUNAddress(ap, true, txid)), txid)
	if got, err := ParseSTUNResponse(good, txid); err != nil || got != ap {
		t.Fatalf("good: %v, %v", got, err)
	}
	other := txid
	other[0] = 9
	if _, err := ParseSTUNResponse(good, other); !errors.Is(err, errSTUNForeign) {
		t.Fatalf("foreign txid: %v", err)
	}
	if _, err := ParseSTUNResponse(good[:10], txid); err == nil {
		t.Fatal("short message must fail")
	}
	noAddr := mk(stunBindingResponse, attr(0x8022, []byte("x")), txid)
	if _, err := ParseSTUNResponse(noAddr, txid); err == nil {
		t.Fatal("reply without address must fail")
	}
	bad := append([]byte(nil), good...)
	binary.BigEndian.PutUint32(bad[4:], 0)
	if _, err := ParseSTUNResponse(bad, txid); err == nil {
		t.Fatal("wrong magic cookie must fail")
	}
	if req := STUNBindingRequest(txid); len(req) != stunHeaderLen || binary.BigEndian.Uint16(req) != stunBindingRequest || [12]byte(req[8:20]) != txid {
		t.Fatalf("request = % x", req)
	}
}
