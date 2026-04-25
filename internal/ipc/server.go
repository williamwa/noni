package ipc

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"sync"

	"github.com/williamwa/noni/internal/proto"
)

// Handler processes a method call. Implementations should return either
// a result value (any JSON-marshalable) or a *proto.RPCError.
type Handler func(method string, params json.RawMessage) (any, error)

type Server struct {
	ln     net.Listener
	h      Handler
	wg     sync.WaitGroup
	closed chan struct{}
}

func NewServer(ln net.Listener, h Handler) *Server {
	return &Server{ln: ln, h: h, closed: make(chan struct{})}
}

func (s *Server) Serve() error {
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			select {
			case <-s.closed:
				return nil
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		s.wg.Add(1)
		go s.handleConn(conn)
	}
}

func (s *Server) Close() error {
	close(s.closed)
	err := s.ln.Close()
	s.wg.Wait()
	return err
}

func (s *Server) handleConn(conn net.Conn) {
	defer s.wg.Done()
	defer conn.Close()
	r := bufio.NewReader(conn)
	enc := json.NewEncoder(conn)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			if err != io.EOF {
				log.Printf("ipc: read: %v", err)
			}
			return
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			_ = enc.Encode(Response{JSONRPC: Version, Error: &Error{Code: -32700, Message: "parse error"}})
			continue
		}
		resp := Response{JSONRPC: Version, ID: req.ID}
		result, err := s.h(req.Method, req.Params)
		if err != nil {
			var rpcErr *proto.RPCError
			if errors.As(err, &rpcErr) {
				resp.Error = &Error{Code: -32000, Message: rpcErr.Message, Data: map[string]string{"code": rpcErr.Code}}
			} else {
				resp.Error = &Error{Code: -32603, Message: err.Error(), Data: map[string]string{"code": proto.EInternal}}
			}
		} else {
			b, mErr := json.Marshal(result)
			if mErr != nil {
				resp.Error = &Error{Code: -32603, Message: "marshal: " + mErr.Error()}
			} else {
				resp.Result = b
			}
		}
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}
