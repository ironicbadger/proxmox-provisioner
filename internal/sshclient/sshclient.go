package sshclient

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/ironicbadger/proxmox-provisioner/internal/config"
	"github.com/ironicbadger/proxmox-provisioner/internal/tailnet"
	"golang.org/x/crypto/ssh"
)

type Client struct {
	cfg    config.SSHConfig
	dialer tailnet.Dialer
	client *ssh.Client
}

type Result struct {
	Command string
	Stdout  string
	Stderr  string
}

type StreamLine struct {
	Text    string
	Replace bool
}

type StreamCallbacks struct {
	Stdout func(StreamLine)
	Stderr func(StreamLine)
}

func New(cfg config.SSHConfig, dialer tailnet.Dialer) (*Client, error) {
	user, hostPort, err := parseTarget(cfg.Target)
	if err != nil {
		return nil, err
	}

	var auth []ssh.AuthMethod
	switch cfg.Auth {
	case "", "auto", "none":
	case "password":
		password := os.Getenv(cfg.PasswordEnv)
		if password == "" {
			return nil, fmt.Errorf("ssh password env %q is empty", cfg.PasswordEnv)
		}
		auth = append(auth, ssh.Password(password))
	default:
		return nil, fmt.Errorf("unsupported ssh auth mode %q", cfg.Auth)
	}
	if cfg.Auth == "auto" && cfg.PasswordEnv != "" {
		if password := os.Getenv(cfg.PasswordEnv); password != "" {
			auth = append(auth, ssh.Password(password))
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return nil, err
	}

	clientConn, chans, reqs, err := ssh.NewClientConn(conn, hostPort, &ssh.ClientConfig{
		User:            user,
		Auth:            auth,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         45 * time.Second,
	})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &Client{
		cfg:    cfg,
		dialer: dialer,
		client: ssh.NewClient(clientConn, chans, reqs),
	}, nil
}

func (c *Client) Close() error {
	if c.client != nil {
		return c.client.Close()
	}
	return nil
}

func (c *Client) RunScript(ctx context.Context, script string, dryRun bool) (Result, error) {
	return c.RunScriptStream(ctx, script, dryRun, StreamCallbacks{})
}

func (c *Client) RunScriptStream(ctx context.Context, script string, dryRun bool, callbacks StreamCallbacks) (Result, error) {
	command := "bash -se"
	if dryRun {
		return Result{Command: command, Stdout: script}, nil
	}
	session, err := c.client.NewSession()
	if err != nil {
		return Result{}, err
	}
	defer session.Close()

	stdoutPipe, err := session.StdoutPipe()
	if err != nil {
		return Result{}, err
	}
	stderrPipe, err := session.StderrPipe()
	if err != nil {
		return Result{}, err
	}
	session.Stdin = strings.NewReader(script)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	streamDone := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	go streamOutput(stdoutPipe, &stdout, callbacks.Stdout, &wg, streamDone)
	go streamOutput(stderrPipe, &stderr, callbacks.Stderr, &wg, streamDone)

	if err := session.Start(command); err != nil {
		return Result{}, err
	}

	done := make(chan error, 1)
	go func() {
		waitErr := session.Wait()
		wg.Wait()
		close(streamDone)
		for streamErr := range streamDone {
			if waitErr == nil && streamErr != nil {
				waitErr = streamErr
			}
		}
		done <- waitErr
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return Result{}, ctx.Err()
	case err := <-done:
		result := Result{
			Command: command,
			Stdout:  stdout.String(),
			Stderr:  stderr.String(),
		}
		if err != nil {
			return result, fmt.Errorf("remote script failed: %w", err)
		}
		return result, nil
	}
}

func streamOutput(reader io.Reader, buffer *bytes.Buffer, callback func(StreamLine), wg *sync.WaitGroup, errs chan<- error) {
	defer wg.Done()

	var current bytes.Buffer
	chunk := make([]byte, 32*1024)
	emit := func(replace bool) {
		if current.Len() == 0 {
			return
		}
		if callback != nil {
			callback(StreamLine{
				Text:    current.String(),
				Replace: replace,
			})
		}
		current.Reset()
	}

	for {
		n, err := reader.Read(chunk)
		if n > 0 {
			data := chunk[:n]
			buffer.Write(data)

			start := 0
			for i, b := range data {
				switch b {
				case '\r':
					current.Write(data[start:i])
					emit(true)
					start = i + 1
				case '\n':
					current.Write(data[start:i])
					emit(false)
					start = i + 1
				}
			}
			if start < len(data) {
				current.Write(data[start:])
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			break
		}
		errs <- err
		return
	}

	emit(false)
}

func (c *Client) UploadText(ctx context.Context, remotePath, content string, dryRun bool) (string, error) {
	command := fmt.Sprintf("install -d %s && cat > %s", shellQuote(dir(remotePath)), shellQuote(remotePath))
	if dryRun {
		return command, nil
	}
	session, err := c.client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	session.Stdin = strings.NewReader(content)

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return "", ctx.Err()
	case err := <-done:
		if err != nil {
			return "", err
		}
		return command, nil
	}
}

func parseTarget(target string) (string, string, error) {
	userHost := strings.SplitN(target, "@", 2)
	if len(userHost) != 2 {
		return "", "", fmt.Errorf("ssh target %q must look like user@host or user@host:port", target)
	}
	user := userHost[0]
	hostPort := userHost[1]
	if _, _, err := net.SplitHostPort(hostPort); err == nil {
		return user, hostPort, nil
	}
	if strings.Count(hostPort, ":") > 1 && !strings.HasPrefix(hostPort, "[") {
		return user, net.JoinHostPort(hostPort, "22"), nil
	}
	if strings.Contains(hostPort, ":") {
		parts := strings.Split(hostPort, ":")
		if len(parts) == 2 && parts[1] != "" {
			return user, hostPort, nil
		}
	}
	return user, net.JoinHostPort(hostPort, "22"), nil
}

func dir(path string) string {
	index := strings.LastIndex(path, "/")
	if index <= 0 {
		return "."
	}
	return path[:index]
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
