package wol

import (
	"bytes"
	"context"
	"net"
	"testing"
	"time"

	"github.com/rusketh/autodeploy/server/internal/match"
	"github.com/rusketh/autodeploy/server/internal/model"
	"github.com/rusketh/autodeploy/server/internal/storage"
)

func TestMagicPacket(t *testing.T) {
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:ff")
	p := MagicPacket(mac)
	if len(p) != 102 {
		t.Fatalf("packet length = %d, want 102", len(p))
	}
	if !bytes.Equal(p[:6], bytes.Repeat([]byte{0xFF}, 6)) {
		t.Errorf("packet must start with 6x 0xFF, got % x", p[:6])
	}
	for i := 0; i < 16; i++ {
		if !bytes.Equal(p[6+i*6:6+(i+1)*6], mac) {
			t.Fatalf("MAC repetition %d wrong: % x", i, p[6+i*6:6+(i+1)*6])
		}
	}
}

// TestWakeMachinesSendsPackets points the waker at a local UDP listener and
// checks one packet arrives per distinct reported MAC (Windows dash format
// included, virtual duplicates deduped, machines without hardware skipped).
func TestWakeMachinesSendsPackets(t *testing.T) {
	ctx := context.Background()
	db, err := storage.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	inv := model.NewInventoryRepo(db)

	withMACs, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := inv.UpdateHardware(ctx, withMACs.ID, model.Hardware{NICs: []model.HardwareNIC{
		{Name: "Ethernet", MAC: "AA-BB-CC-DD-EE-FF"},
		{Name: "Ethernet (alias)", MAC: "aa:bb:cc:dd:ee:ff"}, // same MAC, other format
		{Name: "Wi-Fi", MAC: "11:22:33:44:55:66"},
		{Name: "Bogus", MAC: "not-a-mac"},
	}}); err != nil {
		t.Fatal(err)
	}
	noHardware, err := inv.UpsertFromIdentity(ctx, match.Identity{SystemUUID: "u2"})
	if err != nil {
		t.Fatal(err)
	}

	lis, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	w := &Waker{Inventory: inv, Addr: lis.LocalAddr().String()}
	sent := w.WakeForJobs(ctx, []model.BulkJob{
		{MachineID: withMACs.ID},
		{MachineID: withMACs.ID}, // duplicate machine deduped
		{MachineID: noHardware.ID},
	})
	if sent != 2 {
		t.Fatalf("sent = %d packets, want 2 (two distinct MACs)", sent)
	}

	got := map[string]bool{}
	for i := 0; i < 2; i++ {
		_ = lis.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 256)
		n, _, err := lis.ReadFrom(buf)
		if err != nil {
			t.Fatalf("packet %d not received: %v", i, err)
		}
		if n != 102 {
			t.Fatalf("packet %d length = %d, want 102", i, n)
		}
		got[net.HardwareAddr(buf[6:12]).String()] = true
	}
	if !got["aa:bb:cc:dd:ee:ff"] || !got["11:22:33:44:55:66"] {
		t.Errorf("expected packets for both MACs, got %v", got)
	}
}
