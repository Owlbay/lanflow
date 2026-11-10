package netx

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// A minimal STUN (RFC 5389) client: one Binding Request over UDP, sent
// directly the way a browser's WebRTC stack would (no proxy), and the
// server's view of our address from the reply. Used by the 泄漏检查 to
// see whether UDP leaves through the proxy (TUN) or from the real address.

// STUNServers are the public servers STUNPublicIP tries, in order.
var STUNServers = []string{"stun.l.google.com:19302", "stun.cloudflare.com:3478"}

// stunTimeout bounds one server's exchange.
const stunTimeout = 3 * time.Second

const (
	stunMagicCookie     = 0x2112A442
	stunBindingRequest  = 0x0001
	stunBindingResponse = 0x0101
	stunBindingError    = 0x0111
	attrMappedAddress   = 0x0001
	attrXORMappedAddr   = 0x0020
	attrErrorCode       = 0x0009
	stunHeaderLen       = 20
)

// ErrSTUNUnreachable is returned by STUNPublicIP when no server answered:
// UDP is blocked, or the servers are not reachable from here.
var ErrSTUNUnreachable = errors.New("no STUN server answered")

// STUNPublicIP returns the public address a STUN server sees for a direct
// UDP socket of this machine, trying servers (STUNServers when nil) in
// order. dial (nil = net.Dialer) opens the UDP connection; tests pass a
// dialer that goes to a local fake. ErrSTUNUnreachable wraps the last
// failure when every server timed out or refused.
func STUNPublicIP(ctx context.Context, servers []string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (netip.Addr, error) {
	if len(servers) == 0 {
		servers = STUNServers
	}
	if dial == nil {
		dial = (&net.Dialer{}).DialContext
	}
	var last error
	for _, sv := range servers {
		if ctx.Err() != nil {
			return netip.Addr{}, ctx.Err()
		}
		ap, err := stunBinding(ctx, sv, dial)
		if err == nil {
			return ap.Addr(), nil
		}
		last = err
	}
	return netip.Addr{}, fmt.Errorf("%w: %v", ErrSTUNUnreachable, last)
}

// stunBinding does one Binding exchange with server.
func stunBinding(ctx context.Context, server string, dial func(ctx context.Context, network, addr string) (net.Conn, error)) (netip.AddrPort, error) {
	ctx, cancel := context.WithTimeout(ctx, stunTimeout)
	defer cancel()
	conn, err := dial(ctx, "udp", server)
	if err != nil {
		return netip.AddrPort{}, err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	var txid [12]byte
	if _, err := rand.Read(txid[:]); err != nil {
		return netip.AddrPort{}, err
	}
	if _, err := conn.Write(STUNBindingRequest(txid)); err != nil {
		return netip.AddrPort{}, err
	}
	buf := make([]byte, 1500)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			return netip.AddrPort{}, err
		}
		ap, err := ParseSTUNResponse(buf[:n], txid)
		if errors.Is(err, errSTUNForeign) {
			continue // not ours: keep waiting until the deadline
		}
		return ap, err
	}
}

// STUNBindingRequest encodes a Binding Request with the transaction id.
func STUNBindingRequest(txid [12]byte) []byte {
	msg := make([]byte, stunHeaderLen)
	binary.BigEndian.PutUint16(msg[0:], stunBindingRequest)
	binary.BigEndian.PutUint16(msg[2:], 0)
	binary.BigEndian.PutUint32(msg[4:], stunMagicCookie)
	copy(msg[8:], txid[:])
	return msg
}

var errSTUNForeign = errors.New("stun: reply to another transaction")

// ParseSTUNResponse reads the mapped address from a Binding Response to
// the request with txid: XOR-MAPPED-ADDRESS when present, else
// MAPPED-ADDRESS. Replies to other transactions are rejected with an
// error that stunBinding recognises and skips.
func ParseSTUNResponse(msg []byte, txid [12]byte) (netip.AddrPort, error) {
	if len(msg) < stunHeaderLen {
		return netip.AddrPort{}, errors.New("stun: short message")
	}
	typ := binary.BigEndian.Uint16(msg[0:])
	length := int(binary.BigEndian.Uint16(msg[2:]))
	if binary.BigEndian.Uint32(msg[4:]) != stunMagicCookie {
		return netip.AddrPort{}, errors.New("stun: not a STUN message")
	}
	if [12]byte(msg[8:20]) != txid {
		return netip.AddrPort{}, errSTUNForeign
	}
	if length > len(msg)-stunHeaderLen {
		return netip.AddrPort{}, errors.New("stun: truncated message")
	}
	body := msg[stunHeaderLen : stunHeaderLen+length]
	switch typ {
	case stunBindingResponse:
	case stunBindingError:
		return netip.AddrPort{}, fmt.Errorf("stun: server error %d", errorCode(body))
	default:
		return netip.AddrPort{}, fmt.Errorf("stun: unexpected message type 0x%04x", typ)
	}
	var mapped netip.AddrPort
	for len(body) >= 4 {
		at := binary.BigEndian.Uint16(body[0:])
		al := int(binary.BigEndian.Uint16(body[2:]))
		if al > len(body)-4 {
			break
		}
		val := body[4 : 4+al]
		switch at {
		case attrXORMappedAddr:
			if ap, ok := decodeAddress(val, true, txid); ok {
				return ap, nil
			}
		case attrMappedAddress:
			if ap, ok := decodeAddress(val, false, txid); ok && !mapped.IsValid() {
				mapped = ap
			}
		}
		body = body[4+al+pad4(al):]
	}
	if mapped.IsValid() {
		return mapped, nil
	}
	return netip.AddrPort{}, errors.New("stun: no mapped address in reply")
}

// decodeAddress reads a (XOR-)MAPPED-ADDRESS attribute value.
func decodeAddress(val []byte, xor bool, txid [12]byte) (netip.AddrPort, bool) {
	if len(val) < 8 {
		return netip.AddrPort{}, false
	}
	family := val[1]
	port := binary.BigEndian.Uint16(val[2:])
	if xor {
		port ^= stunMagicCookie >> 16
	}
	switch family {
	case 0x01:
		var a [4]byte
		copy(a[:], val[4:8])
		if xor {
			binary.BigEndian.PutUint32(a[:], binary.BigEndian.Uint32(a[:])^stunMagicCookie)
		}
		return netip.AddrPortFrom(netip.AddrFrom4(a), port), true
	case 0x02:
		if len(val) < 20 {
			return netip.AddrPort{}, false
		}
		var a [16]byte
		copy(a[:], val[4:20])
		if xor {
			var key [16]byte
			binary.BigEndian.PutUint32(key[:], stunMagicCookie)
			copy(key[4:], txid[:])
			for i := range a {
				a[i] ^= key[i]
			}
		}
		return netip.AddrPortFrom(netip.AddrFrom16(a), port), true
	}
	return netip.AddrPort{}, false
}

// errorCode reads the ERROR-CODE attribute of an error response (0 when absent).
func errorCode(body []byte) int {
	for len(body) >= 4 {
		at := binary.BigEndian.Uint16(body[0:])
		al := int(binary.BigEndian.Uint16(body[2:]))
		if al > len(body)-4 {
			return 0
		}
		if at == attrErrorCode && al >= 4 {
			return int(body[6]&0x7)*100 + int(body[7])
		}
		body = body[4+al+pad4(al):]
	}
	return 0
}

// pad4 is the padding that brings an attribute of length n to a 4-byte boundary.
func pad4(n int) int { return (4 - n%4) % 4 }

// EncodeSTUNAddress encodes a (XOR-)MAPPED-ADDRESS attribute value for ap;
// fake servers in tests use it, and it is the inverse of decodeAddress.
func EncodeSTUNAddress(ap netip.AddrPort, xor bool, txid [12]byte) []byte {
	addr := ap.Addr().Unmap()
	port := ap.Port()
	if xor {
		port ^= stunMagicCookie >> 16
	}
	if addr.Is4() {
		a := addr.As4()
		if xor {
			binary.BigEndian.PutUint32(a[:], binary.BigEndian.Uint32(a[:])^stunMagicCookie)
		}
		out := make([]byte, 8)
		out[1] = 0x01
		binary.BigEndian.PutUint16(out[2:], port)
		copy(out[4:], a[:])
		return out
	}
	a := addr.As16()
	if xor {
		var key [16]byte
		binary.BigEndian.PutUint32(key[:], stunMagicCookie)
		copy(key[4:], txid[:])
		for i := range a {
			a[i] ^= key[i]
		}
	}
	out := make([]byte, 20)
	out[1] = 0x02
	binary.BigEndian.PutUint16(out[2:], port)
	copy(out[4:], a[:])
	return out
}
