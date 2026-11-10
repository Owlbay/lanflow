package agent

import (
	"bufio"
	"encoding/binary"
	"io"
	"time"
	"unicode/utf8"

	"lanflow/internal/model"
)

const (
	wsMaxFrames = 1000
	wsTextKeep  = 4 << 10
)

// wsState records the frames of a WebSocket connection (docs/capture.md
// §5.3, M3): opcode and size of each, the first part of text frames.
// Compressed frames (permessage-deflate) are shown as their size only.
type wsState struct {
	c   *connRec
	rec *record
}

// frames parses one direction until the stream ends.
func (w *wsState) frames(br *bufio.Reader, fromPeer bool) {
	var hdr [14]byte
	for {
		if _, err := io.ReadFull(br, hdr[:2]); err != nil {
			return
		}
		fin := hdr[0]&0x80 != 0
		rsv1 := hdr[0]&0x40 != 0
		op := int(hdr[0] & 0x0f)
		masked := hdr[1]&0x80 != 0
		n := int64(hdr[1] & 0x7f)
		switch n {
		case 126:
			if _, err := io.ReadFull(br, hdr[2:4]); err != nil {
				return
			}
			n = int64(binary.BigEndian.Uint16(hdr[2:4]))
		case 127:
			if _, err := io.ReadFull(br, hdr[2:10]); err != nil {
				return
			}
			n = int64(binary.BigEndian.Uint64(hdr[2:10]))
			if n < 0 {
				return
			}
		}
		var mask [4]byte
		if masked {
			if _, err := io.ReadFull(br, mask[:]); err != nil {
				return
			}
		}
		keep := int64(0)
		if (op == 1 || op == 0) && !rsv1 {
			keep = min(n, wsTextKeep)
		}
		payload := make([]byte, keep)
		if _, err := io.ReadFull(br, payload); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, br, n-keep); err != nil {
			return
		}
		if masked {
			for i := range payload {
				payload[i] ^= mask[i%4]
			}
		}
		_ = fin
		f := model.CaptureWSFrame{Time: time.Now(), FromPeer: fromPeer, Opcode: op, Size: n}
		if keep > 0 {
			for len(payload) > 0 && !utf8.Valid(payload) {
				payload = payload[:len(payload)-1] // cut inside a rune
			}
			f.Text = string(payload)
		}
		w.c.st.update(w.rec, func(d *model.CaptureFlowDetail) {
			if len(d.WSFrames) < wsMaxFrames {
				d.WSFrames = append(d.WSFrames, f)
			}
			if fromPeer {
				d.Flow.RespSize += n
			} else {
				d.Flow.ReqSize += n
			}
		})
		if op == 8 { // close
			return
		}
	}
}
