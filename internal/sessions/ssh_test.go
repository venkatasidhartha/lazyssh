package sessions

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"lazyssh/internal/domain"
	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

// fakeSSH is a password-protected SSH server whose shell prints a banner
// and echoes input; "bye" ends it.
func fakeSSH(t *testing.T) int {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PasswordCallback: func(_ ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
		if string(pw) == "s3cret" {
			return nil, nil
		}
		return nil, errors.New("wrong password")
	}}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					ch, creqs, _ := nc.Accept()
					go func() {
						for r := range creqs {
							_ = r.Reply(r.Type == "pty-req" || r.Type == "shell" || r.Type == "window-change", nil)
							if r.Type != "shell" {
								continue
							}
							_, _ = ch.Write([]byte("welcome to fake\r\n"))
							go func() {
								buf := make([]byte, 256)
								for {
									n, err := ch.Read(buf)
									if err != nil {
										return
									}
									_, _ = ch.Write(buf[:n])
									if strings.Contains(string(buf[:n]), "bye") {
										_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
										ch.Close()
										return
									}
								}
							}()
						}
					}()
				}
			}()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// An SSH login made by the service keeps running while lazyssh is closed.
func TestSSHSessionSurvivesDisconnect(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // known_hosts goes there
	t.Setenv("SSH_AUTH_SOCK", "")
	port := fakeSSH(t)
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	srv, err := st.Add(domain.Server{Alias: "fake", Host: "127.0.0.1", Port: port, User: "deploy", Password: "s3cret"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := Listen(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := NewServer(st, sshx.Connector{})
	go func() { _ = s.Serve(ln) }()
	t.Cleanup(func() { s.Shutdown(); ln.Close() })

	c1, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := c1.Open(Spec{Label: "fake", ServerID: srv.ID}, 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	out1 := &sink{}
	r1 := c1.Attach(id, out1, nil)
	eventually(t, "login", func() bool { return strings.Contains(out1.String(), "welcome to fake") })
	_, _ = r1.Write([]byte("hello-remote"))
	eventually(t, "echo", func() bool { return strings.Contains(out1.String(), "hello-remote") })
	c1.Close()

	time.Sleep(100 * time.Millisecond)
	c2, err := Dial(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()
	ended := make(chan struct{})
	out2 := &sink{}
	r2 := c2.Attach(id, out2, func() { close(ended) })
	eventually(t, "replay", func() bool { return strings.Contains(out2.String(), "hello-remote") })
	_, _ = r2.Write([]byte(" still-connected"))
	eventually(t, "same connection", func() bool { return strings.Contains(out2.String(), "still-connected") })
	_, _ = r2.Write([]byte(" bye"))
	select {
	case <-ended:
	case <-time.After(5 * time.Second):
		t.Fatal("end of the remote shell not reported")
	}
	if got, _ := st.Get(srv.ID); got.SSHCount != 1 {
		t.Fatalf("SSH count = %d, want 1", got.SSHCount)
	}
}
