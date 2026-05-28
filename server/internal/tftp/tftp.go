// Package tftp is a minimal read-only TFTP server (RFC 1350) with the
// option-extension subset PXE clients actually use (blksize, tsize,
// timeout — RFCs 2347/2348/2349). It exists so an operator can run
// AutoDeploy and have classic PXE work end-to-end without standing up
// a separate TFTP daemon.
//
// Scope:
//   - RRQ only. WRQ is refused with an error packet. AutoDeploy does
//     not accept TFTP uploads.
//   - octet mode (binary). netascii is treated as octet for the small
//     iPXE bootstrap binaries we serve.
//   - Files are served from a single root directory; path traversal is
//     refused.
//   - blksize (RFC 2348), tsize (RFC 2349), timeout (RFC 2349) are
//     honoured.
//   - Single-threaded transfers per client; each RRQ spawns a goroutine
//     with its own ephemeral UDP socket per RFC 1350.
//
// Out of scope by design:
//   - Multicast TFTP (RFC 2090). The mass-scale story is HTTP mirrors
//     (see docs/user-guide/scaling.md), not TFTP multicast.
//   - Write support.
//   - Per-client rate limiting. TFTP traffic from a PXE chain is tiny
//     (~100KB per machine) and only happens at boot; the HTTP throttle
//     covers the bandwidth-heavy phase.
package tftp

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Opcodes (RFC 1350).
const (
	opRRQ   = 1
	opWRQ   = 2
	opDATA  = 3
	opACK   = 4
	opERROR = 5
	opOACK  = 6
)

// Error codes (RFC 1350 + 2347).
const (
	errNotDefined        = 0
	errFileNotFound      = 1
	errAccessViolation   = 2
	errIllegalOp         = 4
	errUnknownTransferID = 5
	errOptionNegotiation = 8
)

const (
	defaultBlockSize     = 512
	maxBlockSize         = 65464 // RFC 2348 cap minus 4-byte DATA header
	defaultTimeoutSec    = 3
	maxRetries           = 5
	listenReadBufferSize = 65535
)

// Server is the TFTP listener.
type Server struct {
	// Addr is the UDP listen address, e.g. ":69". Operators usually
	// want the privileged port 69; bind via setcap or a systemd unit.
	Addr string
	// Root is the directory files are served from. All requested
	// paths are cleaned and confined to this root.
	Root string
	// Logger receives structured events.
	Logger *slog.Logger

	conn *net.UDPConn
}

// ListenAndServe binds and serves until ctx is cancelled. Returns the
// first non-nil error from listening / shutdown.
func (s *Server) ListenAndServe(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", s.Addr)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", s.Addr, err)
	}
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.Addr, err)
	}
	s.conn = conn
	if s.Logger != nil {
		s.Logger.Info("tftp.listen",
			slog.String("addr", conn.LocalAddr().String()),
			slog.String("root", s.Root))
	}

	// Close the socket on context cancel so the read loop unblocks.
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	buf := make([]byte, listenReadBufferSize)
	for {
		n, client, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		// Copy because the loop reuses buf.
		pkt := make([]byte, n)
		copy(pkt, buf[:n])
		go s.handle(ctx, client, pkt)
	}
}

func (s *Server) handle(ctx context.Context, client *net.UDPAddr, pkt []byte) {
	if len(pkt) < 4 {
		return
	}
	op := binary.BigEndian.Uint16(pkt[:2])
	switch op {
	case opRRQ:
		s.handleRRQ(ctx, client, pkt[2:])
	case opWRQ:
		s.replyError(client, errAccessViolation, "TFTP server is read-only")
	default:
		// Stray packet to the listening port — ignore.
	}
}

// handleRRQ parses the read request, spins up an ephemeral connection
// to the client, and streams the file.
func (s *Server) handleRRQ(ctx context.Context, client *net.UDPAddr, body []byte) {
	filename, mode, opts, err := parseRequest(body)
	if err != nil {
		s.replyError(client, errIllegalOp, "malformed RRQ")
		return
	}
	_ = mode // octet or netascii both treated as raw bytes

	path, err := s.resolve(filename)
	if err != nil {
		s.log("tftp.access_violation",
			slog.String("client", client.String()),
			slog.String("filename", filename),
			slog.String("error", err.Error()))
		s.replyError(client, errAccessViolation, "access denied")
		return
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.replyError(client, errFileNotFound, "not found")
		} else {
			s.replyError(client, errNotDefined, err.Error())
		}
		s.log("tftp.read.miss",
			slog.String("client", client.String()),
			slog.String("filename", filename),
			slog.String("error", err.Error()))
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.replyError(client, errNotDefined, err.Error())
		return
	}

	// Spawn an ephemeral socket as required by RFC 1350.
	xferConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: s.conn.LocalAddr().(*net.UDPAddr).IP})
	if err != nil {
		s.replyError(client, errNotDefined, err.Error())
		return
	}
	defer xferConn.Close()

	tx := &transfer{
		conn:      xferConn,
		client:    client,
		file:      f,
		size:      info.Size(),
		blockSize: defaultBlockSize,
		timeout:   defaultTimeoutSec * time.Second,
		log:       s.Logger,
		filename:  filename,
	}
	tx.applyOptions(opts)
	s.log("tftp.read.start",
		slog.String("client", client.String()),
		slog.String("filename", filename),
		slog.Int64("size", info.Size()),
		slog.Int("block_size", tx.blockSize))

	if err := tx.run(ctx); err != nil {
		s.log("tftp.read.fail",
			slog.String("client", client.String()),
			slog.String("filename", filename),
			slog.String("error", err.Error()))
		return
	}
	s.log("tftp.read.ok",
		slog.String("client", client.String()),
		slog.String("filename", filename),
		slog.Int64("size", info.Size()))
}

func (s *Server) log(action string, attrs ...slog.Attr) {
	if s.Logger == nil {
		return
	}
	all := append([]slog.Attr{slog.String("actor", "tftp"), slog.String("target", "")}, attrs...)
	s.Logger.LogAttrs(context.Background(), slog.LevelInfo, action, all...)
}

// resolve cleans and confines path to s.Root.
func (s *Server) resolve(filename string) (string, error) {
	if filename == "" {
		return "", errors.New("empty filename")
	}
	cleaned := filepath.Clean("/" + filename)
	abs := filepath.Join(s.Root, cleaned)
	rel, err := filepath.Rel(s.Root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes root", filename)
	}
	return abs, nil
}

// replyError sends an ERROR packet from the listening socket. Used
// before we'd otherwise allocate an ephemeral conn.
func (s *Server) replyError(client *net.UDPAddr, code uint16, msg string) {
	buf := make([]byte, 4+len(msg)+1)
	binary.BigEndian.PutUint16(buf[:2], opERROR)
	binary.BigEndian.PutUint16(buf[2:4], code)
	copy(buf[4:], msg)
	_, _ = s.conn.WriteToUDP(buf, client)
}

// parseRequest decodes the RRQ/WRQ body into filename, mode, options.
func parseRequest(body []byte) (filename, mode string, opts map[string]string, err error) {
	parts := strings.Split(string(body), "\x00")
	if len(parts) < 2 || parts[0] == "" {
		return "", "", nil, errors.New("missing filename/mode")
	}
	filename = parts[0]
	mode = strings.ToLower(parts[1])
	opts = map[string]string{}
	for i := 2; i+1 < len(parts); i += 2 {
		k := strings.ToLower(parts[i])
		v := parts[i+1]
		if k == "" {
			continue
		}
		opts[k] = v
	}
	return filename, mode, opts, nil
}

// transfer is one in-flight RRQ on its own ephemeral socket.
type transfer struct {
	conn      *net.UDPConn
	client    *net.UDPAddr
	file      *os.File
	size      int64
	blockSize int
	timeout   time.Duration
	log       *slog.Logger
	filename  string

	negotiatedOpts map[string]string // populated by applyOptions when non-empty
}

func (t *transfer) applyOptions(opts map[string]string) {
	negotiated := map[string]string{}
	if v, ok := opts["blksize"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 8 {
			if n > maxBlockSize {
				n = maxBlockSize
			}
			t.blockSize = n
			negotiated["blksize"] = strconv.Itoa(n)
		}
	}
	if _, ok := opts["tsize"]; ok {
		negotiated["tsize"] = strconv.FormatInt(t.size, 10)
	}
	if v, ok := opts["timeout"]; ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 255 {
			t.timeout = time.Duration(n) * time.Second
			negotiated["timeout"] = strconv.Itoa(n)
		}
	}
	if len(negotiated) > 0 {
		t.negotiatedOpts = negotiated
	}
}

// run executes the transfer.
func (t *transfer) run(ctx context.Context) error {
	// 1. If options were negotiated, send OACK and wait for ACK 0.
	if t.negotiatedOpts != nil {
		if err := t.sendOACK(); err != nil {
			return err
		}
		if err := t.awaitACK(ctx, 0); err != nil {
			return err
		}
	}
	// 2. Send DATA blocks 1..N, awaiting ACK between each.
	block := uint16(1)
	buf := make([]byte, t.blockSize)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		n, err := io.ReadFull(t.file, buf)
		// io.ReadFull returns ErrUnexpectedEOF when fewer than len(buf)
		// bytes remain — that's the final short block.
		if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
			return err
		}
		if err := t.sendDATA(block, buf[:n]); err != nil {
			return err
		}
		if err := t.awaitACK(ctx, block); err != nil {
			return err
		}
		if n < t.blockSize {
			// Final block.
			return nil
		}
		block++
	}
}

func (t *transfer) sendDATA(block uint16, data []byte) error {
	pkt := make([]byte, 4+len(data))
	binary.BigEndian.PutUint16(pkt[:2], opDATA)
	binary.BigEndian.PutUint16(pkt[2:4], block)
	copy(pkt[4:], data)
	for attempt := 0; attempt < maxRetries; attempt++ {
		if _, err := t.conn.WriteToUDP(pkt, t.client); err != nil {
			return err
		}
		return nil
	}
	return errors.New("send data exhausted retries")
}

func (t *transfer) sendOACK() error {
	parts := []string{}
	for k, v := range t.negotiatedOpts {
		parts = append(parts, k, v)
	}
	body := strings.Join(parts, "\x00") + "\x00"
	pkt := make([]byte, 2+len(body))
	binary.BigEndian.PutUint16(pkt[:2], opOACK)
	copy(pkt[2:], body)
	_, err := t.conn.WriteToUDP(pkt, t.client)
	return err
}

// awaitACK reads ACK packets from the client. Retransmits the last
// DATA up to maxRetries times on timeout. The caller is expected to
// have written the matching DATA before calling.
func (t *transfer) awaitACK(ctx context.Context, want uint16) error {
	buf := make([]byte, 64)
	deadline := time.Now().Add(t.timeout)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		_ = t.conn.SetReadDeadline(deadline)
		n, addr, err := t.conn.ReadFromUDP(buf)
		if err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return errors.New("timed out waiting for ACK")
			}
			return err
		}
		// Per RFC 1350: ignore packets from unexpected sources.
		if !addr.IP.Equal(t.client.IP) || addr.Port != t.client.Port {
			continue
		}
		if n < 4 {
			continue
		}
		op := binary.BigEndian.Uint16(buf[:2])
		if op == opERROR {
			return fmt.Errorf("client error: %s", string(buf[4:n]))
		}
		if op != opACK {
			continue
		}
		got := binary.BigEndian.Uint16(buf[2:4])
		if got == want {
			return nil
		}
		// Out-of-order ACK; keep waiting until timeout.
	}
}
