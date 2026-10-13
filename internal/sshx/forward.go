package sshx

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"

	"golang.org/x/crypto/ssh"

	"lazyssh/internal/domain"
)

type ForwardKind string

const (
	ForwardLocal   ForwardKind = "Local"
	ForwardRemote  ForwardKind = "Remote"
	ForwardDynamic ForwardKind = "Dynamic"
)

type ForwardSpec struct {
	Kind     ForwardKind
	Bind     string // listen address (default 127.0.0.1)
	Port     int    // listen port
	Host     string // target host (Local/Remote)
	HostPort int    // target port (Local/Remote)
}

func (f ForwardSpec) String() string {
	bind := f.Bind
	if bind == "" {
		bind = "127.0.0.1"
	}
	switch f.Kind {
	case ForwardLocal:
		return fmt.Sprintf("L %s:%d → %s:%d", bind, f.Port, f.Host, f.HostPort)
	case ForwardRemote:
		return fmt.Sprintf("R %s:%d → %s:%d", bind, f.Port, f.Host, f.HostPort)
	default:
		return fmt.Sprintf("D %s:%d (SOCKS5)", bind, f.Port)
	}
}

type forward struct {
	spec   ForwardSpec
	client *ssh.Client
	ln     net.Listener
}

// Forwarder runs port forwards in-process, so they work for password-only
// servers too.
type Forwarder struct {
	conn *Connector
	mu   sync.Mutex
	fwds map[string][]*forward // by server ID
}

func NewForwarder(c *Connector) *Forwarder {
	return &Forwarder{conn: c, fwds: map[string][]*forward{}}
}

func (f *Forwarder) Active(serverID string) []ForwardSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []ForwardSpec
	for _, fw := range f.fwds[serverID] {
		out = append(out, fw.spec)
	}
	return out
}

func (f *Forwarder) Start(s domain.Server, spec ForwardSpec) error {
	if spec.Bind == "" {
		spec.Bind = "127.0.0.1"
	}
	client, err := f.conn.DialAuto(s)
	if err != nil {
		return err
	}
	listen := net.JoinHostPort(spec.Bind, strconv.Itoa(spec.Port))
	var ln net.Listener
	if spec.Kind == ForwardRemote {
		ln, err = client.Listen("tcp", listen)
	} else {
		ln, err = net.Listen("tcp", listen)
	}
	if err != nil {
		client.Close()
		return err
	}
	fw := &forward{spec: spec, client: client, ln: ln}
	f.mu.Lock()
	f.fwds[s.ID] = append(f.fwds[s.ID], fw)
	f.mu.Unlock()

	go func() {
		// Drop the forward if the SSH connection dies.
		_ = client.Wait()
		ln.Close()
		f.remove(s.ID, fw)
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go fw.handle(c)
		}
	}()
	return nil
}

func (fw *forward) handle(c net.Conn) {
	defer c.Close()
	target := net.JoinHostPort(fw.spec.Host, strconv.Itoa(fw.spec.HostPort))
	var (
		remote net.Conn
		err    error
	)
	switch fw.spec.Kind {
	case ForwardLocal:
		remote, err = fw.client.Dial("tcp", target)
	case ForwardRemote:
		remote, err = net.Dial("tcp", target)
	case ForwardDynamic:
		target, err = socks5Handshake(c)
		if err == nil {
			remote, err = fw.client.Dial("tcp", target)
			socks5Reply(c, err)
		}
	}
	if err != nil {
		return
	}
	defer remote.Close()
	pipe(c, remote)
}

// pipe copies in both directions until both are finished. When one side
// stops sending, only that direction is closed (half-close), so replies
// still in flight are delivered — like OpenSSH does.
func pipe(a, b net.Conn) {
	done := make(chan struct{}, 2)
	cp := func(dst, src net.Conn) {
		_, _ = io.Copy(dst, src)
		if cw, ok := dst.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		} else {
			_ = dst.Close()
		}
		done <- struct{}{}
	}
	go cp(b, a)
	go cp(a, b)
	<-done
	<-done
}

func (f *Forwarder) remove(id string, fw *forward) {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.fwds[id]
	for i, x := range list {
		if x == fw {
			f.fwds[id] = append(list[:i], list[i+1:]...)
			break
		}
	}
	if len(f.fwds[id]) == 0 {
		delete(f.fwds, id)
	}
}

// Stop closes every forward for the server.
func (f *Forwarder) Stop(id string) int {
	f.mu.Lock()
	list := f.fwds[id]
	delete(f.fwds, id)
	f.mu.Unlock()
	for _, fw := range list {
		fw.ln.Close()
		fw.client.Close()
	}
	return len(list)
}

func (f *Forwarder) StopAll() {
	f.mu.Lock()
	ids := make([]string, 0, len(f.fwds))
	for id := range f.fwds {
		ids = append(ids, id)
	}
	f.mu.Unlock()
	for _, id := range ids {
		f.Stop(id)
	}
}

// socks5Handshake implements the no-auth CONNECT subset of SOCKS5.
func socks5Handshake(c net.Conn) (string, error) {
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(c, hdr); err != nil || hdr[0] != 5 {
		return "", errors.New("not socks5")
	}
	if _, err := io.ReadFull(c, make([]byte, hdr[1])); err != nil {
		return "", err
	}
	if _, err := c.Write([]byte{5, 0}); err != nil {
		return "", err
	}
	req := make([]byte, 4)
	if _, err := io.ReadFull(c, req); err != nil {
		return "", err
	}
	if req[1] != 1 { // CONNECT only
		_, _ = c.Write([]byte{5, 7, 0, 1, 0, 0, 0, 0, 0, 0})
		return "", errors.New("unsupported socks command")
	}
	var host string
	switch req[3] {
	case 1:
		b := make([]byte, 4)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 4:
		b := make([]byte, 16)
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = net.IP(b).String()
	case 3:
		l := make([]byte, 1)
		if _, err := io.ReadFull(c, l); err != nil {
			return "", err
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(c, b); err != nil {
			return "", err
		}
		host = string(b)
	default:
		return "", errors.New("bad address type")
	}
	p := make([]byte, 2)
	if _, err := io.ReadFull(c, p); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(p)))), nil
}

func socks5Reply(c net.Conn, err error) {
	code := byte(0)
	if err != nil {
		code = 5
	}
	_, _ = c.Write([]byte{5, code, 0, 1, 0, 0, 0, 0, 0, 0})
}
