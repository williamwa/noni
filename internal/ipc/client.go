package ipc

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"

	"github.com/williamwang/noni/internal/proto"
)

type Client struct {
	conn net.Conn
	r    *bufio.Reader
	id   atomic.Int64
}

func Dial(socketPath string) (*Client, error) {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn, r: bufio.NewReader(conn)}, nil
}

func (c *Client) Close() error { return c.conn.Close() }

// Call sends one request and decodes the result into out.
func (c *Client) Call(method string, params any, out any) error {
	idNum := c.id.Add(1)
	idBytes, _ := json.Marshal(idNum)
	var pBytes json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return err
		}
		pBytes = b
	}
	req := Request{JSONRPC: Version, ID: idBytes, Method: method, Params: pBytes}
	enc := json.NewEncoder(c.conn)
	if err := enc.Encode(req); err != nil {
		return err
	}
	line, err := c.r.ReadBytes('\n')
	if err != nil {
		return err
	}
	var resp Response
	if err := json.Unmarshal(line, &resp); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	if resp.Error != nil {
		code := proto.EInternal
		if m, ok := resp.Error.Data.(map[string]any); ok {
			if c, ok := m["code"].(string); ok {
				code = c
			}
		}
		return proto.NewError(code, resp.Error.Message)
	}
	if out != nil && len(resp.Result) > 0 {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}
