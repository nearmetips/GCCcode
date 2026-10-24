package web

import "testing"

func TestRejectsPublicBind(t *testing.T) {
	if _, err := listenLoopback("0.0.0.0:4725"); err == nil {
		t.Fatal("accepted a non-loopback address")
	}
}
