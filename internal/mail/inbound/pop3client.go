package inbound

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"
)

// pop3Client is a minimal RFC1939 POP3 client sufficient for fetching new
// messages by UIDL. It supports PLAIN and TLS (STLS after greeting) logins.
type pop3Client struct {
	conn net.Conn
	r    *bufio.Reader
}

func dialPOP3(addr, host string, useTLS bool) (*pop3Client, error) {
	var conn net.Conn
	var err error
	dialer := &net.Dialer{Timeout: 15 * time.Second}
	if useTLS {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, &tls.Config{ServerName: host})
	} else {
		conn, err = dialer.Dial("tcp", addr)
	}
	if err != nil {
		return nil, err
	}
	c := &pop3Client{conn: conn, r: bufio.NewReader(conn)}
	if _, err := c.readOK(); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *pop3Client) Quit() {
	_, _ = c.cmd("QUIT")
	_ = c.conn.Close()
}

// noNewlines guards against POP3 command injection (an attacker who controls
// the config could otherwise embed CRLF in USER/PASS). RFC 1939 arguments are
// a single line; reject anything containing CR or LF.
func noNewlines(s string) error {
	if strings.ContainsAny(s, "\r\n") {
		return fmt.Errorf("pop3: argument must not contain newlines")
	}
	return nil
}

func (c *pop3Client) User(user string) error {
	if err := noNewlines(user); err != nil {
		return err
	}
	_, err := c.cmd("USER %s", user)
	return err
}

func (c *pop3Client) Pass(pass string) error {
	if err := noNewlines(pass); err != nil {
		return err
	}
	_, err := c.cmd("PASS %s", pass)
	return err
}

func (c *pop3Client) Stls(host string) error {
	if _, err := c.cmd("STLS"); err != nil {
		return err
	}
	tlsConn := tls.Client(c.conn, &tls.Config{ServerName: host})
	if err := tlsConn.Handshake(); err != nil {
		return err
	}
	c.conn = tlsConn
	c.r = bufio.NewReader(tlsConn)
	return nil
}

// uidl returns the map of message-number → unique id.
func (c *pop3Client) uidl() (map[int]string, error) {
	if err := c.multiline("UIDL"); err != nil {
		return nil, err
	}
	out := map[int]string{}
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "." {
			break
		}
		parts := strings.SplitN(line, " ", 3)
		if len(parts) < 2 {
			continue
		}
		n, err := strconv.Atoi(parts[0])
		if err != nil {
			continue
		}
		out[n] = parts[1]
	}
	return out, nil
}

// retr fetches the full RFC822 body of message number n. The response is
// capped at maxMessageBytes (shared with IMAP and webhook caps); on overflow
// the remaining lines are drained so the POP3 session stays in sync and an
// error is returned so the caller can skip the message.
func (c *pop3Client) retr(n int) ([]byte, error) {
	if err := c.multiline("RETR %d", n); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		if line == ".\r\n" || line == ".\n" {
			break
		}
		// De-stuff: lines beginning with ".." lose one dot.
		if strings.HasPrefix(line, "..") {
			line = line[1:]
		}
		if int64(buf.Len())+int64(len(line)) > maxMessageBytes {
			// Drain the remaining lines so the session stays usable.
			for line != ".\r\n" && line != ".\n" {
				if line, err = c.r.ReadString('\n'); err != nil {
					return nil, err
				}
			}
			return nil, fmt.Errorf("pop3: message exceeds size cap (%d bytes)", maxMessageBytes)
		}
		buf.WriteString(line)
	}
	return buf.Bytes(), nil
}

// dele marks message n for deletion on QUIT.
func (c *pop3Client) dele(n int) error {
	_, err := c.cmd("DELE %d", n)
	return err
}

func (c *pop3Client) multiline(format string, args ...any) error {
	resp, err := c.cmd(format, args...)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(resp, "+OK") {
		return fmt.Errorf("pop3: %s", resp)
	}
	return nil
}

func (c *pop3Client) cmd(format string, args ...any) (string, error) {
	cmd := fmt.Sprintf(format, args...)
	if _, err := c.conn.Write([]byte(cmd + "\r\n")); err != nil {
		return "", err
	}
	return c.readOK()
}

func (c *pop3Client) readOK() (string, error) {
	line, err := c.r.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if !strings.HasPrefix(line, "+") {
		return "", fmt.Errorf("pop3: %s", line)
	}
	return line, nil
}
