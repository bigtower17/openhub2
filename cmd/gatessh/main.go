// gatessh is a minimal SSH server for small embedded Linux boxes (built for
// the Xiaomi Smart Home Hub 2): root shell with a PTY, remote commands and
// SFTP, password (bcrypt) and/or public-key auth.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/subtle"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/creack/pty"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/ssh"
)

var (
	addr     = flag.String("addr", ":22", "listen address")
	dir      = flag.String("dir", "/data/gatessh", "directory with host key, root.hash and authorized_keys")
	shell    = flag.String("shell", "/bin/sh", "login shell")
	setPass  = flag.Bool("set-password", false, "read a password from stdin, store its bcrypt hash in <dir>/root.hash and exit")
	userName = flag.String("user", "root", "the only user name accepted")
)

func main() {
	flag.Parse()
	if err := os.MkdirAll(*dir, 0o700); err != nil {
		log.Fatal(err)
	}
	if *setPass {
		if err := storePassword(os.Stdin); err != nil {
			log.Fatal(err)
		}
		return
	}

	signer, err := hostKey(filepath.Join(*dir, "host_ed25519"))
	if err != nil {
		log.Fatalf("host key: %v", err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback:  checkPassword,
		PublicKeyCallback: checkKey,
		ServerVersion:     "SSH-2.0-gatessh",
	}
	cfg.AddHostKey(signer)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("listening on %s, host key %s", *addr, ssh.FingerprintSHA256(signer.PublicKey()))
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		go serveConn(conn, cfg)
	}
}

func storePassword(r io.Reader) error {
	b, err := io.ReadAll(io.LimitReader(r, 1024))
	if err != nil {
		return err
	}
	pw := strings.TrimRight(string(b), "\r\n")
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	h, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*dir, "root.hash"), h, 0o600)
}

func checkPassword(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
	if c.User() != *userName {
		return nil, errors.New("denied")
	}
	h, err := os.ReadFile(filepath.Join(*dir, "root.hash"))
	if err != nil {
		return nil, errors.New("password auth disabled")
	}
	if bcrypt.CompareHashAndPassword(h, pw) != nil {
		log.Printf("bad password from %s", c.RemoteAddr())
		return nil, errors.New("denied")
	}
	return nil, nil
}

func checkKey(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
	if c.User() != *userName {
		return nil, errors.New("denied")
	}
	data, err := os.ReadFile(filepath.Join(*dir, "authorized_keys"))
	if err != nil {
		return nil, errors.New("denied")
	}
	want := key.Marshal()
	for len(data) > 0 {
		k, _, _, rest, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			break
		}
		if subtle.ConstantTimeCompare(k.Marshal(), want) == 1 {
			return nil, nil
		}
		data = rest
	}
	return nil, errors.New("denied")
}

func hostKey(path string) (ssh.Signer, error) {
	if b, err := os.ReadFile(path); err == nil {
		return ssh.ParsePrivateKey(b)
	}
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	block, err := ssh.MarshalPrivateKey(priv, "gatessh host key")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return nil, err
	}
	return ssh.NewSignerFromKey(priv)
}

func serveConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		conn.Close()
		return
	}
	defer sconn.Close()
	log.Printf("login %s from %s", sconn.User(), sconn.RemoteAddr())
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			continue
		}
		go serveSession(ch, creqs)
	}
}

type session struct {
	ch   ssh.Channel
	mu   sync.Mutex
	ptyf *os.File
	term string
	cols uint32
	rows uint32
	env  []string
}

func serveSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	s := &session{ch: ch}
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "pty-req":
			s.term, s.cols, s.rows = parsePtyReq(req.Payload)
			req.Reply(true, nil)
		case "env":
			var kv struct{ Name, Value string }
			if ssh.Unmarshal(req.Payload, &kv) == nil {
				s.env = append(s.env, kv.Name+"="+kv.Value)
			}
			req.Reply(true, nil)
		case "window-change":
			if len(req.Payload) >= 8 {
				s.resize(binary.BigEndian.Uint32(req.Payload), binary.BigEndian.Uint32(req.Payload[4:]))
			}
		case "shell":
			req.Reply(true, nil)
			go s.run(exec.Command(*shell, "-l"))
		case "exec":
			var p struct{ Command string }
			if ssh.Unmarshal(req.Payload, &p) != nil {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			go s.run(exec.Command(*shell, "-c", p.Command))
		case "subsystem":
			var p struct{ Name string }
			if ssh.Unmarshal(req.Payload, &p) != nil || p.Name != "sftp" {
				req.Reply(false, nil)
				continue
			}
			req.Reply(true, nil)
			go s.sftp()
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

func parsePtyReq(b []byte) (term string, cols, rows uint32) {
	var p struct {
		Term          string
		Cols, Rows    uint32
		Width, Height uint32
		Modes         string
	}
	if ssh.Unmarshal(b, &p) != nil {
		return "xterm", 80, 24
	}
	return p.Term, p.Cols, p.Rows
}

func (s *session) resize(cols, rows uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cols, s.rows = cols, rows
	if s.ptyf != nil {
		_ = pty.Setsize(s.ptyf, &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows)})
	}
}

func (s *session) run(cmd *exec.Cmd) {
	cmd.Env = append([]string{"HOME=/root", "USER=root", "PATH=/bin:/sbin:/usr/bin:/usr/sbin"}, s.env...)
	cmd.Dir = "/"
	var status uint32
	if s.term != "" {
		cmd.Env = append(cmd.Env, "TERM="+s.term)
		f, err := pty.StartWithSize(cmd, &pty.Winsize{Cols: uint16(s.cols), Rows: uint16(s.rows)})
		if err != nil {
			fmt.Fprintf(s.ch.Stderr(), "start: %v\r\n", err)
			s.exit(127)
			return
		}
		s.mu.Lock()
		s.ptyf = f
		s.mu.Unlock()
		go io.Copy(f, s.ch)
		io.Copy(s.ch, f)
		f.Close()
		status = exitStatus(cmd.Wait())
	} else {
		cmd.Stdout = s.ch
		cmd.Stderr = s.ch.Stderr()
		stdin, _ := cmd.StdinPipe()
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(s.ch.Stderr(), "start: %v\n", err)
			s.exit(127)
			return
		}
		go func() { io.Copy(stdin, s.ch); stdin.Close() }()
		status = exitStatus(cmd.Wait())
	}
	s.exit(status)
}

func (s *session) sftp() {
	srv, err := sftp.NewServer(s.ch)
	if err != nil {
		s.exit(1)
		return
	}
	if err := srv.Serve(); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("sftp: %v", err)
	}
	s.exit(0)
}

func (s *session) exit(code uint32) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, code)
	s.ch.SendRequest("exit-status", false, b)
	s.ch.Close()
}

func exitStatus(err error) uint32 {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if ws, ok := ee.Sys().(syscall.WaitStatus); ok {
			return uint32(ws.ExitStatus())
		}
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}
