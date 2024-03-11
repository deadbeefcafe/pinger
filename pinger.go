package pinger

import (
	"bytes"
	"errors"
	"fmt"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/deadbeefcafe/f64stat"
	ping "github.com/digineo/go-ping"
)

type HostCallbackFunc func(h *Host, msg string, arg string)

type Rtt struct {
	T      time.Time
	D      time.Duration
	UpDown int
	Err    error
	Msg    string
	Arg    string
}

func (r *Rtt) String() string {
	if r.Err != nil {
		return fmt.Sprintf("%s %10s %4d %s %s %v", r.T.Format("2006-01-02T15:04:05.000000"), r.D.Truncate(time.Microsecond), r.UpDown, r.Msg, r.Arg, r.Err)
	}
	return fmt.Sprintf("%s %10s %4d %s %s", r.T.Format("2006-01-02T15:04:05.000000"), r.D.Truncate(time.Microsecond), r.UpDown, r.Msg, r.Arg)
}

type Host struct {
	Address         string
	IsUp            bool
	UpDown          int // >1 up <-1 down 0=unknown
	PacketsSent     int
	PacketsReceived int
	UpTime          time.Duration // total amount of time host has been up
	DownTime        time.Duration // total amount of time host has been down
	Interval        time.Duration // time between pings
	MaxRTT          time.Duration // rtt greater than this value will declare a host down
	Timeout         time.Duration // wait this long at most for a pong
	LastRTT         time.Duration // latest RTT value
	Stats           f64stat.Stat  // RTT stats.  min/ave/max/sdev
	C               chan string   // channel to send command to the pinger go routine
	tLastChange     time.Time     // time of last change in Up/Down state
	tLastPacket     time.Time     // time of last change in Up/Down state
	tSent           time.Time     // time last packet sent
	//ltDown           time.Time     // time of start of last DOWN
	//downCount       int           // number of missed pongs
	DownThreshold int    // number of missed pongs to declare host down
	History       []*Rtt // RTT and message history
	HistoryMax    int    // Maximum size of history

	ipaddr   *net.IPAddr
	run      bool
	callback HostCallbackFunc
}

func (h *Host) doCallback(msg string, arg string) {
	if h.callback == nil {
		return
	}
	if msg != "RTT" {
		h.RecordEvent(time.Now(), 0, h.UpDown, nil, msg, arg)
	}
	h.callback(h, msg, arg)
}

/*
func (h *Host) addRTT(rtt time.Duration) {
	up := true
	now := time.Now()
	if rtt >= h.MaxRTT {
		up = false
	} else {
		h.doCallback("RTT", rtt.String())
		h.Stats.Add(rtt.Seconds())
		h.PacketsReceived++
		h.RTT = rtt
	}
	if h.PacketsSent == 0 {
		h.tLastChange = now
		h.tLastPacket = now
		h.IsUp = !up
	}
	h.PacketsSent++

	if up {
		h.UpTime += time.Since(h.tLastPacket)
	} else {
		h.DownTime += time.Since(h.tLastPacket)
	}
	h.tLastPacket = now

	if up != h.IsUp {
		dt := time.Since(h.tLastChange)
		if up {
			d := ""
			if dt > 10*time.Millisecond {
				d = dt.String()
			}
			h.doCallback("UP", d)
		} else {
			d := ""
			if dt > 10*time.Millisecond {
				d = dt.String()
			}
			h.doCallback("DOWN", d)
		}
		h.tLastChange = now
		h.IsUp = up
	}
}
*/

func (h *Host) String() string {
	return fmt.Sprintf("txrx: %d %d loss: %4.1f%% rtt: %.2f/%.2f/%.2f/%.4f up: %s down: %s",
		h.PacketsSent, h.PacketsReceived,
		100.0-100.0*float64(h.PacketsReceived)/float64(h.PacketsSent),
		1000*h.Stats.Min(), 1000*h.Stats.Ave(), 1000*h.Stats.Max(), 1000*h.Stats.Stddev(),
		h.UpTime.Round(time.Second), h.DownTime.Round(time.Second))
}

func (h *Host) RecordEvent(t time.Time, rtt time.Duration, updown int, err error, msg string, arg string) {
	if h.HistoryMax <= 0 {
		return
	}
	h.History = append(h.History, &Rtt{T: t, D: rtt, UpDown: updown, Err: err, Msg: msg, Arg: arg})
	l := len(h.History)
	if l > 10 && l > h.HistoryMax {
		h.History = h.History[10:]
	}
}

func (h *Host) runHostPing(p *Pinger) {
	go func() {
		ticker := time.NewTicker(h.Interval)
		h.run = true
		doping := true
		p.wg.Add(1)
	loop:
		for h.run {
			select {
			case now := <-ticker.C:
				if !doping {
					continue
				}
				h.PacketsSent++
				rtt, err := p.pinger.Ping(h.ipaddr, h.Timeout)
				//h.History = append(h.History, &Rtt{T: now, D: rtt})
				h.RecordEvent(now, rtt, h.UpDown, err, "PING", "")
				if err != nil {
					//msg := fmt.Sprintf("err: %v rtt: %v", err, rtt)
					//if h.UpDown > -5 { h.doCallback("MISS", fmt.Sprintf("%v", err)) }
					if h.UpDown > 0 {
						h.UpDown = 0
					}
					h.UpDown--
					if h.UpDown == -h.DownThreshold {
						h.doCallback("DOWN", "")
					}
					continue
				}
				// Got PONG
				if h.UpDown <= 0 {
					downtime := time.Since(h.tSent)
					h.DownTime += downtime
					h.doCallback("UP", "Was DOWN for "+downtime.String())
					h.UpDown = 0
				}
				h.UpDown++
				if h.UpDown >= 0 {
					h.tSent = now
				}
				h.doCallback("RTT", rtt.String())
				h.Stats.Add(rtt.Seconds())
				h.PacketsReceived++
				h.LastRTT = rtt
			case cmd := <-h.C:
				switch cmd {
				case "cancel":
					h.doCallback("CANCEL", "")
					h.run = false
					break loop
				case "stop":
					h.doCallback("STOP", "")
					doping = false
				case "start":
					h.doCallback("START", "")
					doping = true
				case "reset":
					h.PacketsSent = 0
					h.PacketsReceived = 0
					h.Stats.Reset()
				case "change-interval":
					h.doCallback("INTERVAL", h.Interval.String())
					ticker.Stop()
					ticker = time.NewTicker(h.Interval)
				case "print":
					h.doCallback("PRINT", h.String())
				}
			}
		}
		p.wg.Done()
		ticker.Stop()
	}()
}

func (h *Host) Command(cmd string) {
	select {
	case h.C <- cmd:
	}
}

func (h *Host) Pause() {
	h.Command("stop")
}

func (h *Host) Run() {
	h.Command("start")
}

func (h *Host) Close() {
	h.Command("cancel")
}

func (h *Host) PrintStats() {
	h.Command("print")
}

func (h *Host) ResetStats() {
	h.Command("reset")
}

func (h *Host) SetInterval(d time.Duration) {
	h.Interval = d
	if !h.run {
		return
	}
	h.Command("change-interval")
}

type Pinger struct {
	pinger        *ping.Pinger
	running       bool
	wg            sync.WaitGroup
	hosts         map[string]*Host
	Bind4         string
	Bind6         string
	PayloadSize   uint16
	Interval      time.Duration // default ping interval
	MaxRTT        time.Duration // default host down threshold
	DownThreshold int           // number of missed pongs to declare host down
	HistoryMax    int           // maximum size of history.  0=off
}

func New() *Pinger {
	return &Pinger{
		hosts:         make(map[string]*Host),
		Bind4:         "0.0.0.0",
		Bind6:         "::",
		PayloadSize:   56,
		Interval:      1 * time.Second,
		MaxRTT:        1 * time.Second,
		DownThreshold: 2,
		HistoryMax:    1000,
	}
}

func (p *Pinger) Start() (err error) {
	if p.running {
		return
	}
	log.Printf("XXXX PINGER START")
	p.pinger, err = ping.New(p.Bind4, p.Bind6)
	if err != nil {
		return
	}
	if p.pinger.PayloadSize() != uint16(p.PayloadSize) {
		p.pinger.SetPayloadSize(p.PayloadSize)
	}

	return
}

var ErrHostNotExist = errors.New("host does not exist")

func (p *Pinger) RemoveHost(host string) (err error) {
	h, ok := p.hosts[host]
	if !ok {
		return ErrHostNotExist
	}
	if h.run {
		h.C <- "print"
		h.C <- "cancel"
	}
	delete(p.hosts, host)
	return
}

func (p *Pinger) Stop() {
	for _, h := range p.hosts {
		p.RemoveHost(h.Address)
	}
	p.wg.Wait()
	p.pinger.Close()
	p.running = false
}

var ErrHostExists = errors.New("host already exists")

func (p *Pinger) AddHost(addr string, fx HostCallbackFunc) (h *Host, err error) {

	if _, ok := p.hosts[addr]; ok {
		err = ErrHostExists
		return
	}

	h = &Host{
		Address:       addr,
		ipaddr:        &net.IPAddr{IP: net.ParseIP(addr)},
		Interval:      p.Interval,
		Timeout:       p.Interval - 50*time.Millisecond,
		MaxRTT:        p.MaxRTT,
		C:             make(chan string),
		callback:      fx,
		DownThreshold: p.DownThreshold,
		History:       []*Rtt{},
		HistoryMax:    p.HistoryMax,
	}

	p.hosts[addr] = h
	go h.runHostPing(p)

	return
}

func (p *Pinger) GetHosts() (hosts []*Host) {
	hosts = []*Host{}
	for _, h := range p.hosts {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		return bytes.Compare(hosts[i].ipaddr.IP, hosts[j].ipaddr.IP) < 0
	})
	return
}
