// Package wol sends Wake-on-LAN magic packets so scheduled bulk operations
// can power on their target machines. The MACs come from the hardware
// inventory the agent reports (Hardware.NICs); a machine that has never
// reported hardware simply can't be woken, which is logged and skipped.
package wol

import (
	"context"
	"log/slog"
	"net"
	"strconv"

	"github.com/rusketh/autodeploy/server/internal/model"
)

// DefaultAddr is the standard Wake-on-LAN destination: the limited
// broadcast address on UDP discard port 9. PXE deployment already assumes
// the server shares a broadcast domain with its machines (or has DHCP
// helpers), so the same reachability serves WoL.
const DefaultAddr = "255.255.255.255:9"

// MagicPacket builds the 102-byte WoL payload for a MAC: six 0xFF bytes
// followed by the MAC repeated sixteen times.
func MagicPacket(mac net.HardwareAddr) []byte {
	p := make([]byte, 0, 6+16*len(mac))
	for i := 0; i < 6; i++ {
		p = append(p, 0xFF)
	}
	for i := 0; i < 16; i++ {
		p = append(p, mac...)
	}
	return p
}

// Waker resolves machines to their reported MACs and broadcasts magic
// packets. Construct once in main and share; the zero Addr means
// DefaultAddr.
type Waker struct {
	Inventory *model.InventoryRepo
	Logger    *slog.Logger
	// Addr overrides the packet destination (tests point it at a local
	// listener). Empty = DefaultAddr.
	Addr string
}

// WakeForJobs wakes the distinct machines behind a freshly materialised
// run's jobs. Convenience over WakeMachines for the three call sites that
// hold a job slice.
func (w *Waker) WakeForJobs(ctx context.Context, jobs []model.BulkJob) int {
	seen := map[model.ID]bool{}
	ids := make([]model.ID, 0, len(jobs))
	for _, j := range jobs {
		if !seen[j.MachineID] {
			seen[j.MachineID] = true
			ids = append(ids, j.MachineID)
		}
	}
	return w.WakeMachines(ctx, ids)
}

// WakeMachines sends a magic packet for every distinct MAC the given
// machines have reported. Best-effort: failures are logged, never returned —
// a machine that can't be woken just stays queued (and the operation's
// cancel-after window reaps the job if it never appears). Returns the number
// of packets sent.
func (w *Waker) WakeMachines(ctx context.Context, ids []model.ID) int {
	if w == nil || w.Inventory == nil || len(ids) == 0 {
		return 0
	}
	conn, err := openBroadcastConn(ctx)
	if err != nil {
		w.log().Warn("wol.socket.fail", slog.String("error", err.Error()))
		return 0
	}
	defer conn.Close()
	addr := w.Addr
	if addr == "" {
		addr = DefaultAddr
	}
	dst, err := net.ResolveUDPAddr("udp4", addr)
	if err != nil {
		w.log().Warn("wol.addr.fail", slog.String("error", err.Error()))
		return 0
	}

	sent := 0
	for _, id := range ids {
		m, err := w.Inventory.Get(ctx, id)
		if err != nil {
			w.log().Warn("wol.machine.fail",
				slog.String("actor", "server"),
				slog.String("target", "machine:"+itoa(id)),
				slog.String("error", err.Error()))
			continue
		}
		macs := machineMACs(m)
		if len(macs) == 0 {
			w.log().Info("wol.no_macs",
				slog.String("actor", "server"),
				slog.String("target", "machine:"+itoa(id)),
				slog.String("action", "skipped; machine has not reported a NIC MAC"))
			continue
		}
		for _, mac := range macs {
			if _, err := conn.WriteTo(MagicPacket(mac), dst); err != nil {
				w.log().Warn("wol.send.fail",
					slog.String("actor", "server"),
					slog.String("target", "machine:"+itoa(id)),
					slog.String("error", err.Error()))
				continue
			}
			sent++
		}
		w.log().Info("wol.sent",
			slog.String("actor", "server"),
			slog.String("action", "wake_on_lan"),
			slog.String("target", "machine:"+itoa(id)),
			slog.Int("macs", len(macs)))
	}
	return sent
}

// machineMACs collects the distinct parseable NIC MACs a machine reported.
func machineMACs(m model.MachineRecord) []net.HardwareAddr {
	if m.Hardware == nil {
		return nil
	}
	seen := map[string]bool{}
	var out []net.HardwareAddr
	for _, nic := range m.Hardware.NICs {
		if nic.MAC == "" {
			continue
		}
		hw, err := net.ParseMAC(nic.MAC)
		if err != nil || len(hw) != 6 {
			continue
		}
		key := hw.String()
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, hw)
	}
	return out
}

// openBroadcastConn opens a UDP socket with SO_BROADCAST set so writes to
// the limited broadcast address are permitted (plain net.Dial sockets are
// refused EACCES on most platforms).
func openBroadcastConn(ctx context.Context) (net.PacketConn, error) {
	lc := net.ListenConfig{Control: setBroadcast}
	return lc.ListenPacket(ctx, "udp4", ":0")
}

func itoa(id model.ID) string { return strconv.FormatInt(int64(id), 10) }

func (w *Waker) log() *slog.Logger {
	if w != nil && w.Logger != nil {
		return w.Logger
	}
	return slog.Default()
}
