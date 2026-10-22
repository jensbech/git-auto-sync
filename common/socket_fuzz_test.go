package common

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"gotest.tools/v3/assert"
)

func TestSocketHalfClosedClientGetsAllResponses(t *testing.T) {
	srv := startTestServer(t)
	for i := 0; i < 20; i++ {
		conn, err := net.Dial("unix", srv.Path())
		assert.NilError(t, err)
		_, err = conn.Write([]byte(`{"id":"a","cmd":"version"}` + "\n" + `{"id":"b","cmd":"nope"}` + "\n"))
		assert.NilError(t, err)
		assert.NilError(t, conn.(*net.UnixConn).CloseWrite())
		_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
		data, err := io.ReadAll(conn)
		conn.Close()
		assert.NilError(t, err)
		lines := strings.Split(strings.TrimSpace(string(data)), "\n")
		assert.Equal(t, len(lines), 2, string(data))
		assert.Assert(t, strings.Contains(lines[0], `"id":"a"`))
		assert.Assert(t, strings.Contains(lines[1], `"id":"b"`))
	}
}

func TestSocketOversizedRequestKeepsConnection(t *testing.T) {
	srv := startTestServer(t)
	c := dialRaw(t, srv)

	go func() {
		big := `{"cmd":"version","repo":"` + strings.Repeat("a", MaxRequestSize+10) + `"}` + "\n"
		_, _ = c.conn.Write([]byte(big + `{"id":"after","cmd":"version"}` + "\n"))
	}()

	tooBig := c.mustRead()
	assert.Equal(t, tooBig["ok"], false)
	assert.Equal(t, tooBig["code"], CodeBadRequest)
	assert.Assert(t, strings.Contains(tooBig["error"].(string), "too large"))

	after := c.mustRead()
	assert.Equal(t, after["id"], "after")
	assert.Equal(t, after["ok"], true)
}

func TestSocketBadFieldTypeEchoesID(t *testing.T) {
	srv := startTestServer(t)
	c := dialRaw(t, srv)
	c.send(`{"id":"x","cmd":"events","limit":"ten"}`)
	resp := c.mustRead()
	assert.Equal(t, resp["id"], "x")
	assert.Equal(t, resp["code"], CodeBadRequest)

	client, err := NewSocketClient(srv.Path())
	assert.NilError(t, err)
	defer client.Close()
	_, err = client.conn.Write([]byte("{bad\n"))
	assert.NilError(t, err)
	r, err := client.readResponse("cli-1")
	assert.NilError(t, err)
	assert.Equal(t, ErrorCode(r.Err()), CodeBadRequest)
}

func TestSocketClientDetectsProtocolV1Daemon(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "v1.sock")
	ln, err := net.Listen("unix", path)
	assert.NilError(t, err)
	defer ln.Close()
	replies := []string{`{"error":"unknown command"}`, `{"status":"running","repos":[]}`}
	go func() {
		for _, reply := range replies {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(conn).ReadString('\n')
			_, _ = conn.Write([]byte(reply + "\n"))
			conn.Close()
		}
	}()

	for range replies {
		_, err := CallDaemon(path, Request{Cmd: CmdStatus})
		assert.Assert(t, errors.Is(err, ErrIncompatibleDaemon), "got %v", err)
	}
}

func TestSocketRefusesToReplaceRegularFile(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "notasocket")
	assert.NilError(t, os.WriteFile(path, []byte("keep me"), 0o600))
	_, err := NewSocketServer(path, echoHandler{})
	assert.ErrorContains(t, err, "not a socket")
	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.Equal(t, string(data), "keep me")

	_, err = NewSocketServer("/tmp/"+strings.Repeat("x", 120)+"/d.sock", echoHandler{})
	assert.ErrorContains(t, err, "limit is")
}

func TestSocketInUseIsSentinel(t *testing.T) {
	srv := startTestServer(t)
	_, err := NewSocketServer(srv.Path(), echoHandler{})
	assert.Assert(t, errors.Is(err, ErrSocketInUse))
}

func TestSocketStressConcurrentClients(t *testing.T) {
	srv := startTestServer(t)

	const clients = 50
	const requests = 40
	const events = 120

	errs := make(chan error, clients*2)
	subscribed := make(chan struct{}, clients)
	start := make(chan struct{})
	var wg sync.WaitGroup

	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			client, err := NewSocketClient(srv.Path())
			if err != nil {
				errs <- err
				return
			}
			defer client.Close()

			if i%2 == 0 {
				hello, err := client.Subscribe()
				if err != nil {
					errs <- err
					return
				}
				subscribed <- struct{}{}
				<-start
				last := hello.LastEventID
				_ = client.conn.SetReadDeadline(time.Now().Add(20 * time.Second))
				for last < events {
					frame, err := client.ReadFrame()
					if err != nil {
						errs <- fmt.Errorf("subscriber %d after event %d: %w", i, last, err)
						return
					}
					var ev Event
					if err := json.Unmarshal(frame, &ev); err != nil {
						errs <- err
						return
					}
					if ev.ID == 0 {
						continue
					}
					if ev.ID != last+1 {
						errs <- fmt.Errorf("subscriber %d: got event %d after %d", i, ev.ID, last)
						return
					}
					last = ev.ID
				}
				return
			}

			<-start
			for j := 0; j < requests; j++ {
				cmd := CmdVersion
				if j%5 == 4 {
					cmd = "nope"
				}
				resp, err := client.SendCommand(Request{Cmd: cmd})
				if err != nil {
					errs <- fmt.Errorf("client %d request %d: %w", i, j, err)
					return
				}
				if (cmd == CmdVersion) != resp.OK {
					errs <- fmt.Errorf("client %d request %d: unexpected response %+v", i, j, resp)
					return
				}
			}
		}(i)
	}

	for i := 0; i < clients/2; i++ {
		select {
		case <-subscribed:
		case err := <-errs:
			t.Fatal(err)
		case <-time.After(10 * time.Second):
			t.Fatal("subscribers did not connect")
		}
	}
	close(start)

	for i := 0; i < events; i++ {
		srv.Emit(NewEvent(EventCommit, "/r", fmt.Sprint(i), ""))
		srv.BroadcastState(RepoState{Path: "/r", Status: StatusOK})
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	assert.Equal(t, srv.Events().LastID(), uint64(events))
}

func TestEventLogRecoversFromCorruptLines(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "events.jsonl")

	good := func(id int) string {
		return fmt.Sprintf(`{"id":%d,"type":"commit","repo":"/r","ts":1}`, id)
	}
	content := strings.Join([]string{
		good(1),
		"not json at all",
		good(2),
		"",
		`{"id":0,"type":"commit"}`,
		good(2),
		`{"id":"three"}`,
		`{"id":3,"type":"commit","repo":"/r"` + strings.Repeat(" ", MaxRequestSize+5) + `}`,
		good(4),
		"\xff\xfe\x00",
		`{"id":5,"type":"commit","repo":"/r","ts":1`,
	}, "\n")
	assert.NilError(t, os.WriteFile(path, []byte(content), 0o600))

	l, err := NewEventLog(path, 10)
	assert.NilError(t, err)
	ids := []uint64{}
	for _, ev := range l.Since(0, "", 0) {
		ids = append(ids, ev.ID)
	}
	assert.DeepEqual(t, ids, []uint64{1, 2, 4})
	assert.Equal(t, l.LastID(), uint64(4))

	next := l.Append(NewEvent(EventPush, "/r", "", ""))
	assert.Equal(t, next.ID, uint64(5))
	assert.NilError(t, l.Close())

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	assert.Equal(t, len(lines), 4)
	for _, line := range lines {
		var ev Event
		assert.NilError(t, json.Unmarshal([]byte(line), &ev), line)
	}

	l2, err := NewEventLog(path, 10)
	assert.NilError(t, err)
	defer l2.Close()
	assert.Equal(t, l2.LastID(), uint64(5))
}

func TestEventLogTruncatedTailThenAppend(t *testing.T) {
	path := filepath.Join(shortTempDir(t), "events.jsonl")
	l, err := NewEventLog(path, 10)
	assert.NilError(t, err)
	for i := 0; i < 3; i++ {
		l.Append(NewEvent(EventCommit, "/r", "", ""))
	}
	assert.NilError(t, l.Close())

	data, err := os.ReadFile(path)
	assert.NilError(t, err)
	assert.NilError(t, os.WriteFile(path, data[:len(data)-7], 0o600))

	l2, err := NewEventLog(path, 10)
	assert.NilError(t, err)
	assert.Equal(t, l2.LastID(), uint64(2))
	ev := l2.Append(NewEvent(EventPush, "/r", "", ""))
	assert.Equal(t, ev.ID, uint64(3))
	assert.NilError(t, l2.Close())

	l3, err := NewEventLog(path, 10)
	assert.NilError(t, err)
	defer l3.Close()
	evs := l3.Since(0, "", 0)
	assert.Equal(t, len(evs), 3)
	assert.Equal(t, evs[2].Type, EventPush)
}

func TestReadLine(t *testing.T) {
	r := bufio.NewReaderSize(strings.NewReader("ab\r\n\ncdefgh\nxyz"), 16)
	line, err := ReadLine(r, 4)
	assert.NilError(t, err)
	assert.Equal(t, string(line), "ab")
	line, err = ReadLine(r, 4)
	assert.NilError(t, err)
	assert.Equal(t, string(line), "")
	_, err = ReadLine(r, 4)
	assert.Assert(t, errors.Is(err, ErrRequestTooLarge))
	line, err = ReadLine(r, 4)
	assert.NilError(t, err)
	assert.Equal(t, string(line), "xyz")
	_, err = ReadLine(r, 4)
	assert.Assert(t, errors.Is(err, io.EOF))
}

func FuzzReadLine(f *testing.F) {
	f.Add([]byte("a\nb\n"), 3)
	f.Add([]byte("\xff\xfe\n{\"cmd\":\"x\"}"), 5)
	f.Add([]byte(strings.Repeat("x", 100)+"\nshort\n"), 10)
	f.Add([]byte("\r\n\r\n"), 0)
	f.Fuzz(func(t *testing.T, data []byte, max int) {
		if max < 0 {
			max = -max
		}
		max %= 64

		var want []string
		segments := strings.Split(string(data), "\n")
		if segments[len(segments)-1] == "" {
			segments = segments[:len(segments)-1]
		}
		for _, seg := range segments {
			if len(seg) > max {
				want = append(want, "!toolong")
				continue
			}
			want = append(want, strings.TrimSuffix(seg, "\r"))
		}

		r := bufio.NewReaderSize(bytes.NewReader(data), 16)
		var got []string
		for {
			line, err := ReadLine(r, max)
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, ErrRequestTooLarge) {
				got = append(got, "!toolong")
				continue
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(line) > max || bytes.IndexByte(line, '\n') >= 0 {
				t.Fatalf("bad line %q for max %d", line, max)
			}
			got = append(got, string(line))
		}
		assert.DeepEqual(t, got, want)
	})
}

func FuzzDecodeAndDispatch(f *testing.F) {
	srv := &SocketServer{handler: echoHandler{}, events: NewMemoryEventLog(10)}
	f.Add([]byte(`{"id":"a","cmd":"version"}`))
	f.Add([]byte(`{"cmd":"boom"}`))
	f.Add([]byte(`{"id":7,"cmd":"version"}`))
	f.Add([]byte(`{"id":"x","cmd":"events","limit":"ten","since":-1}`))
	f.Add([]byte(`{"cmd":"set_settings","settings":{"a":1}}`))
	f.Add([]byte("{\"id\":\"\xff\",\"cmd\":\"\xfe\"}"))
	f.Add([]byte(`null`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, line []byte) {
		req, err := DecodeRequest(line)
		var resp Response
		if err != nil {
			resp = ErrResponse(CodeBadRequest, "invalid request: "+err.Error())
		} else {
			resp = srv.dispatch(req)
		}
		resp.ID = req.ID
		if resp.OK == (resp.Code != "") {
			t.Fatalf("response ok=%v but code=%q", resp.OK, resp.Code)
		}
		if !resp.OK && resp.Error == "" {
			t.Fatalf("error response without message: %+v", resp)
		}
		data, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		if bytes.IndexByte(data, '\n') >= 0 {
			t.Fatalf("response contains a newline: %q", data)
		}
		var back ClientResponse
		if err := json.Unmarshal(data, &back); err != nil {
			t.Fatalf("client cannot decode %q: %v", data, err)
		}
		if utf8.ValidString(resp.ID) && back.ID != resp.ID {
			t.Fatalf("id %q came back as %q", resp.ID, back.ID)
		}
	})
}
