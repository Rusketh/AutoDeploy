package tftp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// startTestServer starts a server on a free port and returns its
// address plus a cancel func.
func startTestServer(t *testing.T, root string) (string, context.CancelFunc) {
	t.Helper()
	s := &Server{Addr: "127.0.0.1:0", Root: root}
	// We need the actual bound port — bind manually so we can read it.
	addr, _ := net.ResolveUDPAddr("udp", s.Addr)
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	// Replace the server's Listen logic by invoking the helper path.
	// To keep the test simple we close this socket and let the server
	// reopen on its own ephemeral port the same way.
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	s.Addr = "127.0.0.1:" + strconv.Itoa(port)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.ListenAndServe(ctx) }()
	// Tiny wait so the server is ready before tests fire.
	time.Sleep(50 * time.Millisecond)
	return s.Addr, cancel
}

// tinyTFTPGet fetches a file from the test server using a single
// in-test client. Supports blksize option.
func tinyTFTPGet(t *testing.T, serverAddr, filename string, blksize int) ([]byte, error) {
	t.Helper()
	serv, _ := net.ResolveUDPAddr("udp", serverAddr)
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		return nil, err
	}
	defer c.Close()

	// Build the RRQ packet.
	rrq := []byte{0, opRRQ}
	rrq = append(rrq, []byte(filename)...)
	rrq = append(rrq, 0)
	rrq = append(rrq, []byte("octet")...)
	rrq = append(rrq, 0)
	if blksize > 0 {
		rrq = append(rrq, []byte("blksize")...)
		rrq = append(rrq, 0)
		rrq = append(rrq, []byte(strconv.Itoa(blksize))...)
		rrq = append(rrq, 0)
	}
	if _, err := c.WriteToUDP(rrq, serv); err != nil {
		return nil, err
	}

	bs := defaultBlockSize
	var data bytes.Buffer
	expect := uint16(1)
	buf := make([]byte, 70000)

	// Track server's ephemeral transfer port.
	var xferPort int

	for {
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		n, from, err := c.ReadFromUDP(buf)
		if err != nil {
			return nil, err
		}
		if xferPort == 0 {
			xferPort = from.Port
		}
		op := binary.BigEndian.Uint16(buf[:2])
		switch op {
		case opOACK:
			// Parse OACK to pick up the negotiated blksize.
			oack := string(buf[2:n])
			pairs := strings.Split(strings.TrimRight(oack, "\x00"), "\x00")
			for i := 0; i+1 < len(pairs); i += 2 {
				if strings.EqualFold(pairs[i], "blksize") {
					if v, err := strconv.Atoi(pairs[i+1]); err == nil {
						bs = v
					}
				}
			}
			// ACK block 0.
			ack := []byte{0, opACK, 0, 0}
			if _, err := c.WriteToUDP(ack, from); err != nil {
				return nil, err
			}
		case opDATA:
			blk := binary.BigEndian.Uint16(buf[2:4])
			if blk != expect {
				continue
			}
			data.Write(buf[4:n])
			// ACK.
			ack := []byte{0, opACK, byte(blk >> 8), byte(blk & 0xff)}
			if _, err := c.WriteToUDP(ack, from); err != nil {
				return nil, err
			}
			expect++
			if n-4 < bs {
				return data.Bytes(), nil
			}
		case opERROR:
			return nil, &tftpClientError{code: binary.BigEndian.Uint16(buf[2:4]), msg: string(buf[4:n])}
		}
	}
}

type tftpClientError struct {
	code uint16
	msg  string
}

func (e *tftpClientError) Error() string { return e.msg }

func TestServeSmallFile(t *testing.T) {
	dir := t.TempDir()
	body := []byte("hello, world\n")
	_ = os.WriteFile(filepath.Join(dir, "hello.txt"), body, 0o644)
	addr, cancel := startTestServer(t, dir)
	defer cancel()

	got, err := tinyTFTPGet(t, addr, "hello.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("got %q, want %q", got, body)
	}
}

func TestServeMultiBlockFile(t *testing.T) {
	dir := t.TempDir()
	// 5 KB so we exceed several 512-byte blocks.
	body := make([]byte, 5000)
	_, _ = rand.Read(body)
	_ = os.WriteFile(filepath.Join(dir, "big.bin"), body, 0o644)
	addr, cancel := startTestServer(t, dir)
	defer cancel()

	got, err := tinyTFTPGet(t, addr, "big.bin", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mismatch: %d vs %d bytes", len(got), len(body))
	}
}

func TestServeWithBlksize(t *testing.T) {
	dir := t.TempDir()
	body := make([]byte, 32*1024)
	_, _ = rand.Read(body)
	_ = os.WriteFile(filepath.Join(dir, "big.bin"), body, 0o644)
	addr, cancel := startTestServer(t, dir)
	defer cancel()

	got, err := tinyTFTPGet(t, addr, "big.bin", 1456) // common PXE blksize
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("body mismatch: %d vs %d bytes", len(got), len(body))
	}
}

func TestServeMissingFile(t *testing.T) {
	dir := t.TempDir()
	addr, cancel := startTestServer(t, dir)
	defer cancel()

	_, err := tinyTFTPGet(t, addr, "nope.txt", 0)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	var ce *tftpClientError
	if !errorsAs(err, &ce) {
		t.Fatalf("expected tftpClientError, got %T", err)
	}
	if ce.code != errFileNotFound {
		t.Errorf("code = %d, want %d", ce.code, errFileNotFound)
	}
}

func TestServeRefusesTraversal(t *testing.T) {
	dir := t.TempDir()
	parent := filepath.Dir(dir)
	_ = os.WriteFile(filepath.Join(parent, "secret.txt"), []byte("nope"), 0o644)
	defer os.Remove(filepath.Join(parent, "secret.txt"))

	addr, cancel := startTestServer(t, dir)
	defer cancel()

	// The cleaner collapses "../" so it can never escape; still, the
	// server returning the parent's file is the worst-case bug we
	// test against. A request for "../secret.txt" should resolve
	// to "<root>/secret.txt" and (correctly) fail with file-not-found.
	_, err := tinyTFTPGet(t, addr, "../secret.txt", 0)
	if err == nil {
		t.Fatal("expected error")
	}
	var ce *tftpClientError
	if !errorsAs(err, &ce) {
		t.Fatalf("expected tftpClientError, got %T", err)
	}
	if ce.code != errFileNotFound && ce.code != errAccessViolation {
		t.Errorf("expected file-not-found or access-violation; got code %d msg %q", ce.code, ce.msg)
	}
}

func TestServeRefusesWrite(t *testing.T) {
	dir := t.TempDir()
	addr, cancel := startTestServer(t, dir)
	defer cancel()

	c, _ := net.Dial("udp", addr)
	defer c.Close()
	wrq := []byte{0, opWRQ}
	wrq = append(wrq, []byte("anything")...)
	wrq = append(wrq, 0)
	wrq = append(wrq, []byte("octet")...)
	wrq = append(wrq, 0)
	if _, err := c.Write(wrq); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	_ = c.(net.Conn).SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if op := binary.BigEndian.Uint16(buf[:2]); op != opERROR {
		t.Errorf("expected ERROR, got opcode %d", op)
	}
	if !strings.Contains(string(buf[:n]), "read-only") {
		t.Errorf("error message should mention read-only: %q", buf[:n])
	}
}

// startConfiguredServer starts an already-built server on a free port,
// mirroring startTestServer's bind dance but letting the caller set
// fields like Synth.
func startConfiguredServer(t *testing.T, s *Server) (string, context.CancelFunc) {
	t.Helper()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	_ = conn.Close()
	s.Addr = "127.0.0.1:" + strconv.Itoa(port)
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = s.ListenAndServe(ctx) }()
	time.Sleep(50 * time.Millisecond)
	return s.Addr, cancel
}

func TestSynthServedWhenFileAbsent(t *testing.T) {
	dir := t.TempDir()
	want := []byte("#!ipxe\nsynth body\n")
	s := &Server{
		Root: dir,
		Synth: func(name string) ([]byte, bool) {
			if name == "autoexec.ipxe" {
				return want, true
			}
			return nil, false
		},
	}
	addr, cancel := startConfiguredServer(t, s)
	defer cancel()

	// Both the bare name and a leading-slash form must hit the synth —
	// stock iPXE probes "autoexec.ipxe" then "/autoexec.ipxe".
	for _, name := range []string{"autoexec.ipxe", "/autoexec.ipxe"} {
		got, err := tinyTFTPGet(t, addr, name, 0)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s: got %q, want %q", name, got, want)
		}
	}
}

func TestSynthDoesNotShadowRealFile(t *testing.T) {
	dir := t.TempDir()
	onDisk := []byte("#!ipxe\noperator override\n")
	_ = os.WriteFile(filepath.Join(dir, "autoexec.ipxe"), onDisk, 0o644)
	s := &Server{
		Root: dir,
		Synth: func(name string) ([]byte, bool) {
			return []byte("synth should not win"), true
		},
	}
	addr, cancel := startConfiguredServer(t, s)
	defer cancel()

	got, err := tinyTFTPGet(t, addr, "autoexec.ipxe", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, onDisk) {
		t.Errorf("on-disk file should win: got %q, want %q", got, onDisk)
	}
}

func TestSynthMissFallsThroughToNotFound(t *testing.T) {
	dir := t.TempDir()
	s := &Server{
		Root:  dir,
		Synth: func(name string) ([]byte, bool) { return nil, false },
	}
	addr, cancel := startConfiguredServer(t, s)
	defer cancel()

	_, err := tinyTFTPGet(t, addr, "missing.bin", 0)
	if err == nil {
		t.Fatal("expected file-not-found")
	}
	var ce *tftpClientError
	if !errorsAs(err, &ce) || ce.code != errFileNotFound {
		t.Fatalf("expected file-not-found, got %v", err)
	}
}

// errorsAs is a tiny helper so the test file doesn't have to import
// errors just for one call.
func errorsAs(err error, target any) bool {
	type interfaceErrAs interface {
		As(any) bool
	}
	_ = io.EOF
	tt, ok := target.(**tftpClientError)
	if !ok {
		return false
	}
	for err != nil {
		if ce, ok := err.(*tftpClientError); ok {
			*tt = ce
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			break
		}
		err = u.Unwrap()
	}
	return false
}
