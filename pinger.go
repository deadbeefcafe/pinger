package pinger

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/deadbeefcafe/f64stat"
	ping "github.com/digineo/go-ping"
)

type HostCallbackFunc func(h *Host, msg string, arg string)

type Host struct {
	Address         string
	IsUp            bool
	PacketsSent     int
	PacketsReceived int
	UpTime          time.Duration // total amount of time host has been up
	DownTime        time.Duration // total amount of time host has been down
	Interval        time.Duration // time between pings
	MaxRTT          time.Duration // rtt greater than this value will declare a host down
	Timeout         time.Duration // wait this long at most for a pong
	RTT             time.Duration // latest RTT value
	Stats           f64stat.Stat  // RTT stats.  min/ave/max/sdev
	C               chan string   // channel to send command to the pinger go routine
	tLastChange     time.Time     // time of last change in Up/Down state
	tLastPacket     time.Time     // time of last change in Up/Down state
	ipaddr          *net.IPAddr
	run             bool
	callback        HostCallbackFunc
}

func (h *Host) doCallback(msg string, arg string) {
	if h.callback == nil {
		return
	}
	h.callback(h, msg, arg)
}

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

func (h *Host) String() string {
	return fmt.Sprintf("txrx: %d %d loss: %4.1f%% rtt: %.2f/%.2f/%.2f/%.4f up: %s down: %s",
		h.PacketsSent, h.PacketsReceived,
		100.0-100.0*float64(h.PacketsReceived)/float64(h.PacketsSent),
		1000*h.Stats.Min(), 1000*h.Stats.Ave(), 1000*h.Stats.Max(), 1000*h.Stats.Stddev(),
		h.UpTime.Round(time.Second), h.DownTime.Round(time.Second))
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
			case <-ticker.C:
				if !doping {
					continue
				}
				rtt, err := p.pinger.Ping(h.ipaddr, h.Timeout)
				if err != nil {
					rtt = h.MaxRTT
				}
				h.addRTT(rtt)
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
	pinger      *ping.Pinger
	running     bool
	wg          sync.WaitGroup
	hosts       map[string]*Host
	Bind4       string
	Bind6       string
	PayloadSize uint16
	Interval    time.Duration // default ping interval
	MaxRTT      time.Duration // default host down threshold
}

func New() *Pinger {
	return &Pinger{
		hosts:       make(map[string]*Host),
		Bind4:       "0.0.0.0",
		Bind6:       "::",
		PayloadSize: 56,
		Interval:    1 * time.Second,
		MaxRTT:      1 * time.Second,
	}
}

func (p *Pinger) Start() (err error) {
	if p.running {
		return
	}
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
		Address:  addr,
		ipaddr:   &net.IPAddr{IP: net.ParseIP(addr)},
		Interval: p.Interval,
		Timeout:  p.Interval - 10*time.Millisecond,
		MaxRTT:   p.MaxRTT,
		C:        make(chan string),
		callback: fx,
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
	return
}
