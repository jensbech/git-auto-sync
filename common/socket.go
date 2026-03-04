package common

import (
	"bufio"
	"encoding/json"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
)

type SocketCommand struct {
	Cmd  string `json:"cmd"`
	Repo string `json:"repo,omitempty"`
}

type CommandHandler interface {
	HandleCommand(SocketCommand) (interface{}, error)
}

type SocketServer struct {
	listener net.Listener
	clients  map[net.Conn]struct{}
	mu       sync.RWMutex
	handler  CommandHandler
	done     chan struct{}
}

func DefaultSocketPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", "git-auto-sync", "daemon.sock")
}

func NewSocketServer(socketPath string, handler CommandHandler) (*SocketServer, error) {
	dir := filepath.Dir(socketPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, err
	}

	return &SocketServer{
		listener: listener,
		clients:  make(map[net.Conn]struct{}),
		handler:  handler,
		done:     make(chan struct{}),
	}, nil
}

func (s *SocketServer) Accept() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			select {
			case <-s.done:
				return
			default:
				log.Printf("socket: accept error: %v", err)
				continue
			}
		}

		s.mu.Lock()
		s.clients[conn] = struct{}{}
		s.mu.Unlock()

		go s.handleClient(conn)
	}
}

func (s *SocketServer) handleClient(conn net.Conn) {
	defer func() {
		s.mu.Lock()
		delete(s.clients, conn)
		s.mu.Unlock()
		conn.Close()
	}()

	scanner := bufio.NewScanner(conn)
	for scanner.Scan() {
		var cmd SocketCommand
		if err := json.Unmarshal(scanner.Bytes(), &cmd); err != nil {
			log.Printf("socket: invalid command: %v", err)
			continue
		}

		if s.handler == nil {
			continue
		}

		result, err := s.handler.HandleCommand(cmd)
		if err != nil {
			log.Printf("socket: command error: %v", err)
			resp := map[string]string{"error": err.Error()}
			data, _ := json.Marshal(resp)
			data = append(data, '\n')
			_, _ = conn.Write(data)
			continue
		}

		if result != nil {
			data, err := json.Marshal(result)
			if err != nil {
				log.Printf("socket: marshal error: %v", err)
				continue
			}
			data = append(data, '\n')
			_, _ = conn.Write(data)
		}
	}
}

func (s *SocketServer) Emit(ev Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	data = append(data, '\n')

	s.mu.RLock()
	defer s.mu.RUnlock()

	for conn := range s.clients {
		_, writeErr := conn.Write(data)
		if writeErr != nil {
			go func(c net.Conn) {
				s.mu.Lock()
				delete(s.clients, c)
				s.mu.Unlock()
				c.Close()
			}(conn)
		}
	}
}

func (s *SocketServer) HasClients() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients) > 0
}

func (s *SocketServer) Close() {
	close(s.done)
	s.listener.Close()

	s.mu.Lock()
	for conn := range s.clients {
		conn.Close()
	}
	s.clients = make(map[net.Conn]struct{})
	s.mu.Unlock()

	sockPath := s.listener.Addr().String()
	_ = os.Remove(sockPath)
}
