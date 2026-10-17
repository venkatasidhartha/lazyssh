package sessions

import (
	"os"
	"os/signal"
	"syscall"
	"time"

	"lazyssh/internal/sshx"
	"lazyssh/internal/store"
)

// RunDaemon is the background service process ("lazyssh __sessiond"). It
// keeps running while it has sessions or connected windows, and exits a
// few minutes after the last of both is gone.
func RunDaemon(dir string, st *store.Store, conn sshx.Connector) error {
	ln, err := Listen(dir)
	if err != nil {
		return err
	}
	s := NewServer(st, conn)
	go func() { _ = s.Serve(ln) }()

	signal.Ignore(syscall.SIGHUP, syscall.SIGPIPE)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			s.Shutdown()
			ln.Close()
			_ = os.Remove(SocketPath(dir))
			return nil
		case <-s.stopped:
			time.Sleep(100 * time.Millisecond) // let the reply go out
			ln.Close()
			_ = os.Remove(SocketPath(dir))
			return nil
		case <-tick.C:
			if s.Idle(3 * time.Minute) {
				ln.Close()
				_ = os.Remove(SocketPath(dir))
				return nil
			}
		}
	}
}
