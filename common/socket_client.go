package common

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"time"
)

var ErrIncompatibleDaemon = errors.New("the running daemon is an older version; install the matching daemon (`just install`) and run `git-auto-sync daemon restart`")

type ClientResponse struct {
	ID     string          `json:"id,omitempty"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
	Code   string          `json:"code,omitempty"`
}

func (r *ClientResponse) Err() error {
	if r.OK {
		return nil
	}
	return &CodedError{Code: r.Code, Msg: r.Error}
}

func (r *ClientResponse) Decode(v interface{}) error {
	if len(r.Result) == 0 {
		return errors.New("empty result")
	}
	return json.Unmarshal(r.Result, v)
}

type SocketClient struct {
	conn    net.Conn
	scanner *bufio.Scanner
	seq     int
	Timeout time.Duration
}

func NewSocketClient(socketPath string) (*SocketClient, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	return &SocketClient{
		conn:    conn,
		scanner: scanner,
		Timeout: 30 * time.Second,
	}, nil
}

func (c *SocketClient) write(req Request) error {
	data, err := json.Marshal(req)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	_, err = c.conn.Write(data)
	return err
}

func (c *SocketClient) readLine() ([]byte, error) {
	if c.scanner.Scan() {
		return c.scanner.Bytes(), nil
	}
	if err := c.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, io.EOF
}

func (c *SocketClient) readResponse(id string) (*ClientResponse, error) {
	if err := c.conn.SetReadDeadline(time.Now().Add(c.Timeout)); err != nil {
		return nil, err
	}
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}

	var probe struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(line, &probe)
	if probe.Type == FrameHello {
		return nil, ErrIncompatibleDaemon
	}

	var resp ClientResponse
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.ID == "" && id != "" && resp.Code == "" {
		return nil, ErrIncompatibleDaemon
	}
	if resp.ID != id && (resp.ID != "" || resp.OK) {
		return nil, fmt.Errorf("response id mismatch: sent %q, got %q", id, resp.ID)
	}
	return &resp, nil
}

func (c *SocketClient) nextID(req *Request) {
	if req.ID == "" {
		c.seq++
		req.ID = "cli-" + strconv.Itoa(c.seq)
	}
}

func (c *SocketClient) SendCommand(req Request) (*ClientResponse, error) {
	c.nextID(&req)
	if err := c.write(req); err != nil {
		return nil, err
	}
	return c.readResponse(req.ID)
}

func (c *SocketClient) Subscribe() (*Hello, error) {
	req := Request{Cmd: CmdSubscribe}
	c.nextID(&req)
	if err := c.write(req); err != nil {
		return nil, err
	}
	resp, err := c.readResponse(req.ID)
	if err != nil {
		return nil, err
	}
	if err := resp.Err(); err != nil {
		return nil, err
	}

	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	var hello Hello
	if err := json.Unmarshal(line, &hello); err != nil {
		return nil, err
	}
	if hello.Type != FrameHello {
		return nil, fmt.Errorf("expected hello frame, got %q", hello.Type)
	}
	if err := c.conn.SetReadDeadline(time.Time{}); err != nil {
		return nil, err
	}
	return &hello, nil
}

func (c *SocketClient) ReadFrame() (json.RawMessage, error) {
	line, err := c.readLine()
	if err != nil {
		return nil, err
	}
	return json.RawMessage(append([]byte(nil), line...)), nil
}

func (c *SocketClient) Close() {
	c.conn.Close()
}

func CallDaemon(socketPath string, req Request) (*ClientResponse, error) {
	client, err := NewSocketClient(socketPath)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return client.SendCommand(req)
}
