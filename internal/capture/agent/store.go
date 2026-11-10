package agent

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"lanflow/internal/model"
)

// store is the capture ring (docs/capture.md §7.1): at most maxFlows
// records; bodies stay in memory up to memLimit, beyond which the bodies
// of the oldest finished records are sealed (AES-256-GCM, key only in
// memory) into a spill file of at most spillLimit bytes, deleted on exit.
type store struct {
	mu       sync.Mutex
	recs     []*record // oldest first
	byID     map[int64]*record
	nextID   int64
	seq      int64 // bumped on every change
	epoch    int64 // changes on clear: clients resync
	maxFlows int
	bodyCap  int64
	memLimit int64
	memUsed  int64

	spillDir   string
	spillLimit int64
	spill      *os.File
	spillPath  string
	spillOff   int64
	aead       cipher.AEAD
	onChange   func()
}

type record struct {
	d   model.CaptureFlowDetail
	upd int64
	// spilled bodies
	spilled         bool
	spillAt, spillN int64
	dropped         bool // bodies lost (spill file full)
}

func newStore(maxFlows int, bodyCap, memLimit int64, spillDir string, spillLimit int64) *store {
	if maxFlows <= 0 {
		maxFlows = 5000
	}
	if bodyCap <= 0 {
		bodyCap = 1 << 20
	}
	if memLimit <= 0 {
		memLimit = 64 << 20
	}
	if spillLimit <= 0 {
		spillLimit = 512 << 20
	}
	return &store{byID: map[int64]*record{}, maxFlows: maxFlows, bodyCap: bodyCap, memLimit: memLimit,
		spillDir: spillDir, spillLimit: spillLimit, epoch: time.Now().UnixNano()}
}

// CleanSpill removes spill files left by a crashed agent.
func CleanSpill(dir string) {
	if dir == "" {
		return
	}
	ms, _ := filepath.Glob(filepath.Join(dir, "session-*.bin"))
	for _, m := range ms {
		os.Remove(m)
	}
}

// add creates a record and returns its ID.
func (s *store) add(f model.CaptureFlow) *record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	s.seq++
	f.ID = s.nextID
	r := &record{d: model.CaptureFlowDetail{Flow: f}, upd: s.seq}
	s.recs = append(s.recs, r)
	s.byID[f.ID] = r
	for len(s.recs) > s.maxFlows {
		old := s.recs[0]
		s.recs[0] = nil
		s.recs = s.recs[1:]
		delete(s.byID, old.d.Flow.ID)
		s.memUsed -= int64(len(old.d.ReqBody) + len(old.d.RespBody))
	}
	s.changed()
	return r
}

func (s *store) changed() {
	if s.onChange != nil {
		s.onChange() // must not block
	}
}

// update runs fn on the record under the lock.
func (s *store) update(r *record, fn func(d *model.CaptureFlowDetail)) {
	s.mu.Lock()
	before := int64(len(r.d.ReqBody) + len(r.d.RespBody))
	fn(&r.d)
	after := int64(len(r.d.ReqBody) + len(r.d.RespBody))
	s.memUsed += after - before
	s.seq++
	r.upd = s.seq
	if s.memUsed > s.memLimit {
		s.spillLocked()
	}
	s.mu.Unlock()
}

// appendBody adds p to a body, keeping at most bodyCap bytes and counting
// the full size.
func (s *store) appendBody(r *record, resp bool, p []byte) {
	s.update(r, func(d *model.CaptureFlowDetail) {
		body, size := &d.ReqBody, &d.ReqBodySize
		if resp {
			body, size = &d.RespBody, &d.RespBodySize
		}
		*size += int64(len(p))
		if room := s.bodyCap - int64(len(*body)); room > 0 {
			if int64(len(p)) > room {
				p = p[:room]
			}
			*body = append(*body, p...)
		}
		if *size > s.bodyCap && !d.Flow.Has(model.FlowTruncated) {
			d.Flow.Flags = append(d.Flow.Flags, model.FlowTruncated)
		}
		if resp {
			d.Flow.RespSize = *size
		} else {
			d.Flow.ReqSize = *size
		}
	})
}

// spillLocked moves bodies of the oldest finished records to the spill
// file until memory use is under three quarters of the limit.
func (s *store) spillLocked() {
	target := s.memLimit * 3 / 4
	for _, r := range s.recs {
		if s.memUsed <= target {
			return
		}
		if !r.d.Flow.Done || r.spilled || r.dropped || len(r.d.ReqBody)+len(r.d.RespBody) == 0 {
			continue
		}
		n := int64(len(r.d.ReqBody) + len(r.d.RespBody))
		if err := s.sealLocked(r); err != nil {
			r.dropped = true // no room / no file: the bodies are lost
		}
		r.d.ReqBody, r.d.RespBody = nil, nil
		s.memUsed -= n
	}
}

func (s *store) sealLocked(r *record) error {
	if s.spillDir == "" {
		return errors.New("no spill directory")
	}
	if s.spill == nil {
		key := make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		blk, _ := aes.NewCipher(key)
		aead, _ := cipher.NewGCM(blk)
		if err := os.MkdirAll(s.spillDir, 0o700); err != nil {
			return err
		}
		p := filepath.Join(s.spillDir, fmt.Sprintf("session-%d.bin", time.Now().UnixNano()))
		f, err := os.OpenFile(p, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		s.spill, s.spillPath, s.aead = f, p, aead
	}
	plain := make([]byte, 4, 4+len(r.d.ReqBody)+len(r.d.RespBody))
	binary.BigEndian.PutUint32(plain, uint32(len(r.d.ReqBody)))
	plain = append(append(plain, r.d.ReqBody...), r.d.RespBody...)
	nonce := make([]byte, s.aead.NonceSize())
	rand.Read(nonce)
	sealed := s.aead.Seal(nonce, nonce, plain, nil)
	if s.spillOff+int64(len(sealed)) > s.spillLimit {
		return errors.New("spill file full")
	}
	if _, err := s.spill.WriteAt(sealed, s.spillOff); err != nil {
		return err
	}
	r.spilled, r.spillAt, r.spillN = true, s.spillOff, int64(len(sealed))
	s.spillOff += int64(len(sealed))
	return nil
}

// detail returns a copy of the record with its bodies.
func (s *store) detail(id int64) (model.CaptureFlowDetail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.byID[id]
	if !ok {
		return model.CaptureFlowDetail{}, false
	}
	d := r.d
	d.Flow.Flags = append([]string(nil), r.d.Flow.Flags...)
	d.ReqHeader = append([]model.KV(nil), r.d.ReqHeader...)
	d.RespHeader = append([]model.KV(nil), r.d.RespHeader...)
	d.WSFrames = append([]model.CaptureWSFrame(nil), r.d.WSFrames...)
	d.ReqBody = append([]byte(nil), r.d.ReqBody...)
	d.RespBody = append([]byte(nil), r.d.RespBody...)
	if r.spilled && s.spill != nil {
		buf := make([]byte, r.spillN)
		if _, err := s.spill.ReadAt(buf, r.spillAt); err == nil {
			ns := s.aead.NonceSize()
			if plain, err := s.aead.Open(nil, buf[:ns], buf[ns:], nil); err == nil && len(plain) >= 4 {
				n := int(binary.BigEndian.Uint32(plain))
				if 4+n <= len(plain) {
					d.ReqBody = plain[4 : 4+n]
					d.RespBody = plain[4+n:]
				}
			}
		}
	}
	return d, true
}

// since returns the summaries changed after seq, the current seq, the
// oldest ID kept and the epoch.
func (s *store) since(seq, epoch int64) (flows []model.CaptureFlow, cur, minID, ep int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if epoch != s.epoch {
		seq = 0
	}
	for _, r := range s.recs {
		if r.upd > seq {
			f := r.d.Flow
			f.Flags = append([]string(nil), f.Flags...)
			flows = append(flows, f)
		}
	}
	if len(s.recs) > 0 {
		minID = s.recs[0].d.Flow.ID
	} else {
		minID = s.nextID + 1
	}
	return flows, s.seq, minID, s.epoch
}

func (s *store) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

// clear forgets everything and deletes the spill file.
func (s *store) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs, s.byID, s.memUsed = nil, map[int64]*record{}, 0
	s.epoch++
	s.seq++
	s.closeSpillLocked()
	s.changed()
}

func (s *store) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeSpillLocked()
}

func (s *store) closeSpillLocked() {
	if s.spill != nil {
		s.spill.Close()
		os.Remove(s.spillPath)
		s.spill, s.aead, s.spillOff = nil, nil, 0
	}
	for _, r := range s.recs {
		if r.spilled {
			r.spilled, r.dropped = false, true
		}
	}
}
