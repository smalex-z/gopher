package main

import (
	"net"
	"testing"
)

// TestPortListening verifies the /proc/net parser: a bound TCP listener reads as
// listening (and stops after close), a bound UDP socket reads as listening, and
// the two protocols don't cross-report.
func TestPortListening(t *testing.T) {
	// TCP LISTEN → listening; after close → not.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tcpPort := l.Addr().(*net.TCPAddr).Port
	if !portListening(tcpPort, "tcp") {
		t.Errorf("tcp %d has a listener but portListening=false", tcpPort)
	}
	if portListening(tcpPort, "udp") {
		t.Errorf("tcp listener %d must not appear as a udp bind", tcpPort)
	}
	_ = l.Close()
	if portListening(tcpPort, "tcp") {
		t.Errorf("tcp %d was closed but still reads as listening", tcpPort)
	}

	// UDP bound socket → listening.
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	udpPort := pc.LocalAddr().(*net.UDPAddr).Port
	if !portListening(udpPort, "udp") {
		t.Errorf("udp %d is bound but portListening=false", udpPort)
	}
	if portListening(udpPort, "tcp") {
		t.Errorf("udp bind %d must not appear as a tcp listener", udpPort)
	}
}
