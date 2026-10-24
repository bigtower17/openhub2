package deploy

import (
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// File describes one file to upload.
type File struct {
	Local  string // path on the build machine
	Remote string // absolute path on the hub
	Mode   os.FileMode
}

// Run connects to the hub via SSH (root, no password) and uploads all files.
func Run(host string, port int, files []File) error {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	log.Printf("    Connecting to %s ...", addr)
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	defer conn.Close()

	sc, err := sftp.NewClient(conn)
	if err != nil {
		return fmt.Errorf("sftp: %w", err)
	}
	defer sc.Close()

	for _, f := range files {
		if err := upload(sc, f); err != nil {
			return err
		}
	}
	return nil
}

// Exec runs a command on the hub and returns combined output.
func Exec(host string, port int, cmd string) (string, error) {
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	cfg := &ssh.ClientConfig{
		User:            "root",
		Auth:            []ssh.AuthMethod{ssh.Password("")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	defer conn.Close()

	sess, err := conn.NewSession()
	if err != nil {
		return "", err
	}
	defer sess.Close()

	out, err := sess.CombinedOutput(cmd)
	return string(out), err
}

func upload(sc *sftp.Client, f File) error {
	src, err := os.Open(f.Local)
	if err != nil {
		return fmt.Errorf("open %s: %w", f.Local, err)
	}
	defer src.Close()

	info, err := src.Stat()
	if err != nil {
		return err
	}

	dir := path.Dir(f.Remote)
	sc.MkdirAll(dir)

	dst, err := sc.Create(f.Remote)
	if err != nil {
		return fmt.Errorf("create %s: %w", f.Remote, err)
	}
	n, err := io.Copy(dst, src)
	if err != nil {
		dst.Close()
		return fmt.Errorf("write %s: %w", f.Remote, err)
	}
	dst.Close()

	if err := sc.Chmod(f.Remote, f.Mode); err != nil {
		return fmt.Errorf("chmod %s: %w", f.Remote, err)
	}

	log.Printf("    %s → %s (%s)", f.Local, f.Remote, humanSize(info.Size()))
	_ = n
	return nil
}

func humanSize(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
