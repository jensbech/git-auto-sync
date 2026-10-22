package common

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	ClientQueueSize    = 256
	ClientWriteTimeout = 2 * time.Second
	MaxRequestSize     = 1024 * 1024
	MaxSocketPathLen   = 103
)

var (
	ErrSocketInUse     = errors.New("another daemon is already listening")
	ErrRequestTooLarge = fmt.Errorf("request too large (limit %d bytes)", MaxRequestSize)
)

type CommandHandler interface {
	HandleCommand(Request) Response
}

type socketConn struct {
	conn  net.Conn
	queue chan []byte
	done  chan struct{}
	once  sync.Once
}

func newSocketConn(conn net.Conn) *socketConn {
	return &socketConn{
		conn:  conn,
		queue: make(chan []byte, ClientQueueSize),
		done:  make(chan struct{}),
	}
}

func (c *socketConn) close() {
	c.once.Do(func() {
		close(c.done)
		c.conn.Close()
	})
}

func (c *socketConn) send(data []byte) bool {
	select {
	case <-c.done:
		return false
	default:
	}
	select {
	case c.queue <- data:
		return true
	default:
		c.close()
		return false
	}
}

func (c *socketConn) finish() {
	close(c.queue)
}

func (c *socketConn) writeLoop() {
	for {
		select {
		case <-c.done:
			return
		case data, ok := <-c.queue:
			if !ok {
				c.close()
				return
			}
			_ = c.conn.SetWriteDeadline(time.Now().Add(ClientWriteTimeout))
			if _, err := c.conn.Write(data); err != nil {
				c.close()
				return
			}
		}
	}
}

type SocketServer struct {
	listener  net.Listener
	path      string
	handler   CommandHandler
	events    *EventLog
	mu        sync.Mutex
	conns     map[*socketConn]struct{}
	subs      map[*socketConn]struct{}
	hello     func() Hello
	done      chan struct{}
	closeOnce sync.Once
}

const SocketEnvVar = "GIT_AUTO_SYNC_SOCKET"

func DefaultSocketPath() string {
	if p := os.Getenv(SocketEnvVar); p != "" {
		return p
	}
	return filepath.Join(StateDir(), "daemon.sock")
}

func CheckSocketPath(socketPath string) error {
	if len(socketPath) > MaxSocketPathLen {
		return fmt.Errorf("socket path is %d bytes, the limit is %d: %s", len(socketPath), MaxSocketPathLen, socketPath)
	}
	return nil
}

func NewSocketServer(socketPath string, handler CommandHandler) (*SocketServer, error) {
	if err := CheckSocketPath(socketPath); err != nil {
		return nil, err
	}
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}

	if conn, err := net.DialTimeout("unix", socketPath, 500*time.Millisecond); err == nil {
		conn.Close()
		return nil, fmt.Errorf("%w on %s", ErrSocketInUse, socketPath)
	}
	if info, err := os.Lstat(socketPath); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return nil, fmt.Errorf("refusing to replace %s: it exists and is not a socket", socketPath)
		}
		if err := os.Remove(socketPath); err != nil {
			return nil, err
		}
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(socketPath, 0600); err != nil {
		listener.Close()
		return nil, err
	}

	return &SocketServer{
		listener: listener,
		path:     socketPath,
		handler:  handler,
		events:   NewMemoryEventLog(EventLogCapacity),
		conns:    make(map[*socketConn]struct{}),
		subs:     make(map[*socketConn]struct{}),
		done:     make(chan struct{}),
	}, nil
}

func (s *SocketServer) Path() string {
	return s.path
}

func (s *SocketServer) SetEventLog(l *EventLog) {
	if l == nil {
		return
	}
	s.mu.Lock()
	s.events = l
	s.mu.Unlock()
}

func (s *SocketServer) Events() *EventLog {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.events
}

func (s *SocketServer) SetHelloProvider(fn func() Hello) {
	s.mu.Lock()
	s.hello = fn
	s.mu.Unlock()
}

func (s *SocketServer) Accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return
			}
			log.Printf("socket: accept error: %v", err)
			time.Sleep(100 * time.Millisecond)
			continue
		}

		c := newSocketConn(conn)
		s.mu.Lock()
		select {
		case <-s.done:
			s.mu.Unlock()
			conn.Close()
			return
		default:
		}
		s.conns[c] = struct{}{}
		s.mu.Unlock()

		go c.writeLoop()
		go s.handleClient(c)
	}
}

func (s *SocketServer) handleClient(c *socketConn) {
	defer func() {
		s.mu.Lock()
		delete(s.conns, c)
		delete(s.subs, c)
		s.mu.Unlock()
		c.finish()
	}()

	r := bufio.NewReaderSize(c.conn, 64*1024)
	for {
		line, err := ReadLine(r, MaxRequestSize)
		if errors.Is(err, ErrRequestTooLarge) {
			if !sendJSON(c, ErrResponse(CodeBadRequest, err.Error())) {
				return
			}
			continue
		}
		if err != nil {
			return
		}
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		req, err := DecodeRequest(line)
		if err != nil {
			resp := ErrResponse(CodeBadRequest, "invalid request: "+err.Error())
			resp.ID = req.ID
			if !sendJSON(c, resp) {
				return
			}
			continue
		}

		if req.Cmd == CmdSubscribe {
			if !s.subscribe(c, req) {
				return
			}
			continue
		}

		resp := s.dispatch(req)
		resp.ID = req.ID
		if !sendJSON(c, resp) {
			return
		}
	}
}

func ReadLine(r *bufio.Reader, max int) ([]byte, error) {
	var line []byte
	tooLong := false
	for {
		chunk, err := r.ReadSlice('\n')
		if !tooLong {
			content := len(line) + len(chunk)
			if err == nil {
				content--
			}
			if content > max {
				tooLong = true
				line = nil
			} else {
				line = append(line, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil && (!errors.Is(err, io.EOF) || (len(line) == 0 && !tooLong)) {
			return nil, err
		}
		if tooLong {
			return nil, ErrRequestTooLarge
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		return bytes.TrimSuffix(line, []byte{'\r'}), nil
	}
}

func DecodeRequest(line []byte) (Request, error) {
	var req Request
	err := json.Unmarshal(line, &req)
	return req, err
}

func (s *SocketServer) dispatch(req Request) (resp Response) {
	if req.Cmd == "" {
		return ErrResponse(CodeBadRequest, "missing cmd")
	}
	if s.handler == nil {
		return ErrResponse(CodeUnknownCmd, "unknown command: "+req.Cmd)
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("socket: panic handling %q: %v", req.Cmd, r)
			resp = ErrResponse(CodeUnknown, fmt.Sprintf("internal error: %v", r))
		}
	}()
	return s.handler.HandleCommand(req)
}

func (s *SocketServer) subscribe(c *socketConn, req Request) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	var hello Hello
	if s.hello != nil {
		hello = s.hello()
	}
	hello.Type = FrameHello
	hello.Protocol = ProtocolVersion
	if hello.Repos == nil {
		hello.Repos = []RepoState{}
	}
	hello.LastEventID = s.events.LastID()

	resp := OKResponse(VersionResult{Protocol: ProtocolVersion, Version: hello.Version})
	resp.ID = req.ID
	if !sendJSON(c, resp) || !sendJSON(c, hello) {
		return false
	}
	s.subs[c] = struct{}{}
	return true
}

func sendJSON(c *socketConn, v interface{}) bool {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("socket: marshal error: %v", err)
		return true
	}
	return c.send(append(data, '\n'))
}

func (s *SocketServer) pushLocked(data []byte) {
	for c := range s.subs {
		if !c.send(data) {
			delete(s.subs, c)
		}
	}
}

func (s *SocketServer) Emit(ev Event) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ev = s.events.Append(ev)
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	s.pushLocked(append(data, '\n'))
}

func (s *SocketServer) Broadcast(payload interface{}) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	s.pushLocked(data)
}

func (s *SocketServer) BroadcastState(rs RepoState) {
	s.Broadcast(StateFrame{Type: FrameState, Repo: rs.Path, Ts: time.Now().Unix(), Data: rs})
}

func (s *SocketServer) HasClients() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}

func (s *SocketServer) Close() {
	s.closeOnce.Do(func() {
		close(s.done)
		s.listener.Close()

		s.mu.Lock()
		for c := range s.conns {
			c.close()
		}
		s.conns = make(map[*socketConn]struct{})
		s.subs = make(map[*socketConn]struct{})
		events := s.events
		s.mu.Unlock()

		if events != nil {
			_ = events.Close()
		}
	})
}
