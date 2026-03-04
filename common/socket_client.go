package common

import (
	"bufio"
	"encoding/json"
	"net"
	"time"
)

type SocketClient struct {
	conn    net.Conn
	scanner *bufio.Scanner
}

func NewSocketClient(socketPath string) (*SocketClient, error) {
	conn, err := net.DialTimeout("unix", socketPath, 2*time.Second)
	if err != nil {
		return nil, err
	}

	return &SocketClient{
		conn:    conn,
		scanner: bufio.NewScanner(conn),
	}, nil
}

func (c *SocketClient) SendCommand(cmd SocketCommand) (json.RawMessage, error) {
	data, err := json.Marshal(cmd)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')

	if err := c.conn.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	if _, err := c.conn.Write(data); err != nil {
		return nil, err
	}

	if err := c.conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return nil, err
	}
	if c.scanner.Scan() {
		return json.RawMessage(c.scanner.Bytes()), nil
	}
	if err := c.scanner.Err(); err != nil {
		return nil, err
	}
	return nil, nil
}

func (c *SocketClient) Close() {
	c.conn.Close()
}
