package pinger

import (
	"log"
	"testing"
	"time"
)

func TestPing(t *testing.T) {
	log.SetFlags(log.Lmicroseconds)
	pinger := New()
	pinger.Start()

	printCB := func(h *Host, msg string, arg string) {
		switch msg {
		case "RTT", "CANCEL":
			return
		}
		if msg == "PRINT" {
			log.Printf("%-20.20s %s", h.Address, arg)
			return
		}
		log.Printf("%-20.20s %s %s", h.Address, msg, arg)
	}

	hosts := []string{
		"::1",
		"127.0.0.1",
		"10.128.0.1",
		"172.24.0.1",
		"172.25.0.1",
		"172.24.10.3",
		"1.1.1.1",
		"8.8.8.8",
		"192.168.12.4",
		"10.128.7.164",
		"10.128.8.136",
		"fd07::207c:14a1:e3e0",
	}

	for _, ip := range hosts {
		_, err := pinger.AddHost(ip, printCB)
		if err != nil {
			t.Fatalf("AddHost %s failed : %v", ip, err)
		}
	}

	ip := "172.24.100.2"
	h, err := pinger.AddHost(ip, printCB)
	if err != nil {
		t.Fatalf("AddHost %s failed : %v", ip, err)
	}

	time.Sleep(25 * time.Second)
	h.SetInterval(500 * time.Millisecond)
	time.Sleep(3 * time.Second)
	h.Pause()
	time.Sleep(3 * time.Second)
	h.Run()
	time.Sleep(3 * time.Second)
	h.PrintStats()
	time.Sleep(3 * time.Second)
	h.Close()
	time.Sleep(3 * time.Second)

	for i, h := range pinger.GetHosts() {
		log.Printf("%3d %-20.20s %s", i, h.Address, h.String())
	}

	pinger.Stop()

}
