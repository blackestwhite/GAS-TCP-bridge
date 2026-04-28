package client

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"gas-tcp-bridge/internal/logging"
	"gas-tcp-bridge/internal/protocol"
	"gas-tcp-bridge/internal/session"
)

const (
	ModeRaw    = "raw"
	ModeSOCKS5 = "socks5"
)

type Config struct {
	Listen          string
	RelayURL        string
	SIDParam        string
	Mode            string
	ChunkSize       int
	PollInterval    time.Duration
	RequestTimeout  time.Duration
	Token           string
	FrontDial       string
	FrontSNI        string
	FrontHost       string
	FrontForceHTTP1 bool
	Logger          *logging.Logger
}

type Client struct {
	cfg      Config
	listener net.Listener
	http     *http.Client
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	logger   *logging.Logger
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	cfg = withDefaults(cfg)
	if cfg.RelayURL == "" {
		return nil, errors.New("relay URL is required")
	}
	if cfg.Mode != ModeRaw && cfg.Mode != ModeSOCKS5 {
		return nil, fmt.Errorf("unsupported mode %q", cfg.Mode)
	}
	relayURL, err := url.ParseRequestURI(cfg.RelayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid relay URL: %w", err)
	}
	if err := normalizeFrontConfig(&cfg, relayURL); err != nil {
		return nil, err
	}

	listener, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(ctx)
	c := &Client{
		cfg:      cfg,
		listener: listener,
		http:     newHTTPClient(cfg, relayURL),
		cancel:   cancel,
		logger:   cfg.Logger,
	}
	c.wg.Add(1)
	go c.acceptLoop(ctx)
	return c, nil
}

func (c *Client) Addr() string {
	return c.listener.Addr().String()
}

func (c *Client) Close() error {
	c.cancel()
	err := c.listener.Close()
	c.wg.Wait()
	return err
}

func withDefaults(cfg Config) Config {
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:1080"
	}
	if cfg.Mode == "" {
		cfg.Mode = ModeSOCKS5
	}
	if cfg.SIDParam == "" {
		cfg.SIDParam = "bsid"
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 16 * 1024
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 100 * time.Millisecond
	}
	if cfg.RequestTimeout <= 0 {
		cfg.RequestTimeout = 20 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = logging.New(logging.Info)
	}
	return cfg
}

func (c *Client) acceptLoop(ctx context.Context) {
	defer c.wg.Done()
	c.logger.Infof("client listening on %s mode=%s", c.Addr(), c.cfg.Mode)
	for {
		conn, err := c.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.logger.Warnf("accept failed: %v", err)
			continue
		}
		c.wg.Add(1)
		go func() {
			defer c.wg.Done()
			c.handleConn(ctx, conn)
		}()
	}
}

type connState struct {
	client     *Client
	sid        string
	local      net.Conn
	pending    *session.PendingQueue
	ctx        context.Context
	cancel     context.CancelFunc
	seqMu      sync.Mutex
	nextSeq    uint64
	downMu     sync.Mutex
	downAck    uint64
	downBuffer map[uint64]protocol.Message
}

func (c *Client) handleConn(parent context.Context, conn net.Conn) {
	defer conn.Close()

	ctx, cancel := context.WithCancel(parent)
	st := &connState{
		client:     c,
		sid:        newSessionID(),
		local:      conn,
		pending:    session.NewPendingQueue(4096, 64*1024*1024),
		ctx:        ctx,
		cancel:     cancel,
		nextSeq:    1,
		downBuffer: make(map[uint64]protocol.Message),
	}
	defer cancel()
	go func() {
		<-ctx.Done()
		_ = conn.Close()
	}()

	var open protocol.Message
	if c.cfg.Mode == ModeSOCKS5 {
		host, port, err := readSOCKS5Connect(conn, c.cfg.RequestTimeout)
		if err != nil {
			c.logger.Warnf("socks5 handshake failed sid=%s: %v", st.sid, err)
			return
		}
		open = st.newMessage(protocol.TypeOpen, nil, host, port, "")
	} else {
		open = st.newMessage(protocol.TypeOpen, nil, "", 0, "")
	}
	if err := st.pending.Enqueue(open); err != nil {
		c.logger.Warnf("enqueue open failed sid=%s: %v", st.sid, err)
		return
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		st.sendLoop()
	}()
	go func() {
		defer wg.Done()
		st.pollLoop()
	}()

	st.readLoop()
	st.enqueueClose()
	st.waitPendingDrained(minDuration(2*time.Second, c.cfg.RequestTimeout))
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(c.cfg.RequestTimeout):
		cancel()
		<-done
	}
}

func (st *connState) readLoop() {
	buf := make([]byte, st.client.cfg.ChunkSize)
	for {
		n, err := st.local.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if enqueueErr := st.enqueueData(data); enqueueErr != nil {
				st.client.logger.Warnf("enqueue data failed sid=%s: %v", st.sid, enqueueErr)
				st.cancel()
				return
			}
		}
		if err != nil {
			if !errors.Is(err, io.EOF) && st.ctx.Err() == nil {
				st.client.logger.Debugf("local read ended sid=%s: %v", st.sid, err)
			}
			return
		}
	}
}

func (st *connState) sendLoop() {
	retryAfter := retryInterval(st.client.cfg.PollInterval)
	ticker := time.NewTicker(retryAfter / 2)
	defer ticker.Stop()

	backoff := st.client.cfg.PollInterval
	for {
		due := st.pending.Due(time.Now(), retryAfter)
		if len(due) > 0 {
			failed := false
			for _, msg := range due {
				st.pending.MarkAttempt(msg.Seq, time.Now())
				ack, err := st.postUp(msg)
				if err != nil {
					st.client.logger.Warnf("upload failed sid=%s seq=%d type=%s: %v", st.sid, msg.Seq, msg.Type, err)
					failed = true
					break
				}
				if ack.Error != "" {
					st.client.logger.Warnf("broker rejected sid=%s seq=%d: %s", st.sid, msg.Seq, ack.Error)
					st.cancel()
					return
				}
				st.pending.AckThrough(ack.Ack)
				backoff = st.client.cfg.PollInterval
			}
			if failed {
				if !sleepContext(st.ctx, backoff) {
					return
				}
				backoff = minDuration(backoff*2, 3*time.Second)
				continue
			}
		}

		select {
		case <-st.ctx.Done():
			return
		case <-st.pending.Notify():
		case <-ticker.C:
		}
	}
}

func (st *connState) pollLoop() {
	backoff := st.client.cfg.PollInterval
	for {
		resp, err := st.getDown()
		if err != nil {
			st.client.logger.Warnf("poll failed sid=%s: %v", st.sid, err)
			if !sleepContext(st.ctx, backoff) {
				return
			}
			backoff = minDuration(backoff*2, 3*time.Second)
			continue
		}
		backoff = st.client.cfg.PollInterval
		if resp.Ack > 0 {
			st.pending.AckThrough(resp.Ack)
		}
		if len(resp.Chunks) == 0 {
			if !sleepContext(st.ctx, st.client.cfg.PollInterval) {
				return
			}
			continue
		}
		if err := st.handleDownChunks(resp.Chunks); err != nil {
			st.client.logger.Warnf("downstream handling failed sid=%s: %v", st.sid, err)
			st.cancel()
			return
		}
	}
}

func (st *connState) postUp(msg protocol.Message) (protocol.AckResponse, error) {
	var body bytes.Buffer
	if err := protocol.EncodeMessage(&body, msg); err != nil {
		return protocol.AckResponse{}, err
	}
	req, err := http.NewRequestWithContext(st.ctx, http.MethodPost, st.relayURL("up", 0), &body)
	if err != nil {
		return protocol.AckResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if st.client.cfg.Token != "" {
		req.Header.Set("X-Bridge-Token", st.client.cfg.Token)
	}
	resp, err := st.client.http.Do(req)
	if err != nil {
		return protocol.AckResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocol.AckResponse{}, fmt.Errorf("relay status %d", resp.StatusCode)
	}
	return protocol.DecodeAckResponse(resp.Body)
}

func (st *connState) getDown() (protocol.DownResponse, error) {
	req, err := http.NewRequestWithContext(st.ctx, http.MethodGet, st.relayURL("down", st.currentDownAck()), nil)
	if err != nil {
		return protocol.DownResponse{}, err
	}
	if st.client.cfg.Token != "" {
		req.Header.Set("X-Bridge-Token", st.client.cfg.Token)
	}
	resp, err := st.client.http.Do(req)
	if err != nil {
		return protocol.DownResponse{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return protocol.DownResponse{}, fmt.Errorf("relay status %d", resp.StatusCode)
	}
	return protocol.DecodeDownResponse(resp.Body)
}

func (st *connState) relayURL(op string, ack uint64) string {
	u, _ := url.Parse(st.client.cfg.RelayURL)
	q := u.Query()
	q.Set("op", op)
	q.Set(st.client.cfg.SIDParam, st.sid)
	if op == "down" {
		q.Set("ack", strconv.FormatUint(ack, 10))
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (st *connState) handleDownChunks(chunks []protocol.Message) error {
	st.downMu.Lock()
	defer st.downMu.Unlock()

	for _, msg := range chunks {
		if msg.Seq <= st.downAck {
			continue
		}
		if msg.SID != st.sid {
			return fmt.Errorf("sid mismatch %q", msg.SID)
		}
		st.downBuffer[msg.Seq] = msg
	}

	for {
		next := st.downAck + 1
		msg, ok := st.downBuffer[next]
		if !ok {
			return nil
		}
		delete(st.downBuffer, next)
		switch msg.Type {
		case protocol.TypeData:
			data, err := protocol.DecodeBytes(msg.Data)
			if err != nil {
				return err
			}
			if _, err := st.local.Write(data); err != nil {
				return err
			}
		case protocol.TypeClose:
			st.downAck = next
			st.cancel()
			_ = st.local.Close()
			return nil
		case protocol.TypeError:
			st.downAck = next
			_ = st.local.Close()
			return fmt.Errorf("broker error: %s", msg.Message)
		default:
			return fmt.Errorf("unexpected downstream message type %q", msg.Type)
		}
		st.downAck = next
	}
}

func (st *connState) currentDownAck() uint64 {
	st.downMu.Lock()
	defer st.downMu.Unlock()
	return st.downAck
}

func (st *connState) enqueueData(data []byte) error {
	return st.pending.Enqueue(st.newMessage(protocol.TypeData, data, "", 0, ""))
}

func (st *connState) enqueueClose() {
	_ = st.pending.Enqueue(st.newMessage(protocol.TypeClose, nil, "", 0, ""))
}

func (st *connState) waitPendingDrained(timeout time.Duration) {
	if timeout <= 0 {
		return
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for st.pending.Len() > 0 {
		select {
		case <-st.ctx.Done():
			return
		case <-deadline.C:
			return
		case <-ticker.C:
		}
	}
}

func (st *connState) newMessage(typ string, data []byte, targetHost string, targetPort int, text string) protocol.Message {
	st.seqMu.Lock()
	seq := st.nextSeq
	st.nextSeq++
	st.seqMu.Unlock()

	msg := protocol.Message{
		SID:        st.sid,
		Seq:        seq,
		Type:       typ,
		TargetHost: targetHost,
		TargetPort: targetPort,
		Message:    text,
	}
	if data != nil {
		msg.Data = protocol.EncodeBytes(data)
	}
	return msg
}

func readSOCKS5Connect(conn net.Conn, timeout time.Duration) (string, int, error) {
	if timeout > 0 {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		defer conn.SetReadDeadline(time.Time{})
	}

	head := make([]byte, 2)
	if _, err := io.ReadFull(conn, head); err != nil {
		return "", 0, err
	}
	if head[0] != 0x05 {
		return "", 0, errors.New("unsupported SOCKS version")
	}
	methods := make([]byte, int(head[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", 0, err
	}
	hasNoAuth := false
	for _, m := range methods {
		if m == 0x00 {
			hasNoAuth = true
			break
		}
	}
	if !hasNoAuth {
		_, _ = conn.Write([]byte{0x05, 0xff})
		return "", 0, errors.New("SOCKS5 no-auth method not offered")
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return "", 0, err
	}

	req := make([]byte, 4)
	if _, err := io.ReadFull(conn, req); err != nil {
		return "", 0, err
	}
	if req[0] != 0x05 {
		return "", 0, errors.New("unsupported SOCKS request version")
	}
	if req[1] != 0x01 {
		_ = writeSOCKS5Reply(conn, 0x07)
		return "", 0, errors.New("only CONNECT is supported")
	}
	if req[2] != 0x00 {
		_ = writeSOCKS5Reply(conn, 0x01)
		return "", 0, errors.New("invalid SOCKS reserved byte")
	}

	var host string
	switch req[3] {
	case 0x01:
		addr := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return "", 0, err
		}
		host = net.IP(addr).String()
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return "", 0, err
		}
		name := make([]byte, int(l[0]))
		if _, err := io.ReadFull(conn, name); err != nil {
			return "", 0, err
		}
		host = string(name)
	case 0x04:
		addr := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, addr); err != nil {
			return "", 0, err
		}
		host = net.IP(addr).String()
	default:
		_ = writeSOCKS5Reply(conn, 0x08)
		return "", 0, errors.New("unsupported address type")
	}

	var portBytes [2]byte
	if _, err := io.ReadFull(conn, portBytes[:]); err != nil {
		return "", 0, err
	}
	port := int(binary.BigEndian.Uint16(portBytes[:]))
	if port == 0 {
		_ = writeSOCKS5Reply(conn, 0x04)
		return "", 0, errors.New("invalid target port")
	}
	if err := writeSOCKS5Reply(conn, 0x00); err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func writeSOCKS5Reply(conn net.Conn, code byte) error {
	_, err := conn.Write([]byte{0x05, code, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	return err
}

func newSessionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return strconv.FormatInt(time.Now().UnixNano(), 36)
}

func retryInterval(poll time.Duration) time.Duration {
	d := poll * 10
	if d < 500*time.Millisecond {
		return 500 * time.Millisecond
	}
	if d > 3*time.Second {
		return 3 * time.Second
	}
	return d
}

func sleepContext(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
