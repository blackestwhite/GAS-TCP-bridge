package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"gas-tcp-bridge/internal/logging"
	"gas-tcp-bridge/internal/protocol"
	"gas-tcp-bridge/internal/session"
)

type Config struct {
	Listen         string
	SessionTimeout time.Duration
	MaxDownBatch   int
	ChunkSize      int
	FixedUpstream  string
	DialNetwork    string
	Token          string
	LogLevel       string
	DialTimeout    time.Duration
	Logger         *logging.Logger
}

type Broker struct {
	cfg    Config
	logger *logging.Logger

	mu       sync.Mutex
	sessions map[string]*Session

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
}

func NewBroker(cfg Config) *Broker {
	cfg = withDefaults(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	b := &Broker{
		cfg:      cfg,
		logger:   cfg.Logger,
		sessions: make(map[string]*Session),
		ctx:      ctx,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	go b.cleanupLoop()
	return b
}

func withDefaults(cfg Config) Config {
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if cfg.SessionTimeout <= 0 {
		cfg.SessionTimeout = 60 * time.Second
	}
	if cfg.MaxDownBatch <= 0 {
		cfg.MaxDownBatch = 256 * 1024
	}
	if cfg.ChunkSize <= 0 {
		cfg.ChunkSize = 16 * 1024
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	if cfg.DialNetwork == "" {
		cfg.DialNetwork = "tcp"
	}
	if cfg.Logger == nil {
		cfg.Logger = logging.New(logging.Info)
	}
	return cfg
}

func (b *Broker) Close() {
	b.cancel()
	<-b.done

	b.mu.Lock()
	sessions := make([]*Session, 0, len(b.sessions))
	for _, s := range b.sessions {
		sessions = append(sessions, s)
	}
	b.sessions = make(map[string]*Session)
	b.mu.Unlock()
	for _, s := range sessions {
		s.Close()
	}
}

func (b *Broker) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/up", b.handleUp)
	mux.HandleFunc("/down", b.handleDown)
	mux.HandleFunc("/healthz", b.handleHealthz)
	return mux
}

func (b *Broker) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}

func (b *Broker) handleUp(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !b.authorized(w, r) {
		return
	}

	msg, err := protocol.DecodeMessage(r.Body)
	if err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if err := protocol.ValidateMessage(msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if sid := r.URL.Query().Get("sid"); sid != "" && sid != msg.SID {
		http.Error(w, "sid mismatch", http.StatusBadRequest)
		return
	}

	s := b.getOrCreateSession(msg.SID)
	ack, acceptErr := s.Accept(msg)
	resp := protocol.AckResponse{SID: msg.SID, Ack: ack}
	if acceptErr != nil {
		resp.Error = acceptErr.Error()
	}
	writeJSON(w, resp)
}

func (b *Broker) handleDown(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !b.authorized(w, r) {
		return
	}

	sid := r.URL.Query().Get("sid")
	if sid == "" {
		http.Error(w, "sid is required", http.StatusBadRequest)
		return
	}
	ack, err := parseUintQuery(r, "ack")
	if err != nil {
		http.Error(w, "invalid ack", http.StatusBadRequest)
		return
	}

	s := b.getSession(sid)
	if s == nil {
		writeJSON(w, protocol.DownResponse{SID: sid, Ack: 0, Chunks: []protocol.Message{}})
		return
	}
	writeJSON(w, s.DownResponse(ack, b.cfg.MaxDownBatch))
}

func (b *Broker) authorized(w http.ResponseWriter, r *http.Request) bool {
	if b.cfg.Token == "" {
		return true
	}
	if r.Header.Get("X-Bridge-Token") == b.cfg.Token {
		return true
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
	return false
}

func (b *Broker) getOrCreateSession(sid string) *Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	if s, ok := b.sessions[sid]; ok {
		return s
	}
	s := newSession(sid, b.cfg, b.logger)
	b.sessions[sid] = s
	return s
}

func (b *Broker) getSession(sid string) *Session {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sessions[sid]
}

func (b *Broker) cleanupLoop() {
	defer close(b.done)
	interval := b.cfg.SessionTimeout / 2
	if interval < time.Second {
		interval = time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-b.ctx.Done():
			return
		case <-ticker.C:
			b.cleanupIdle()
		}
	}
}

func (b *Broker) cleanupIdle() {
	now := time.Now()
	var expired []*Session

	b.mu.Lock()
	for sid, s := range b.sessions {
		if s.IdleFor(now) > b.cfg.SessionTimeout {
			delete(b.sessions, sid)
			expired = append(expired, s)
		}
	}
	b.mu.Unlock()

	for _, s := range expired {
		b.logger.Infof("cleaning idle session sid=%s", s.sid)
		s.Close()
	}
}

type Session struct {
	sid    string
	cfg    Config
	logger *logging.Logger

	mu             sync.Mutex
	clientAck      uint64
	nextServerSeq  uint64
	inboundPending map[uint64]protocol.Message
	outbound       *session.PendingQueue
	upstream       net.Conn
	writeCh        chan []byte
	lastActive     time.Time
	closed         bool
	done           chan struct{}
	closeOnce      sync.Once
}

func newSession(sid string, cfg Config, logger *logging.Logger) *Session {
	return &Session{
		sid:            sid,
		cfg:            cfg,
		logger:         logger,
		nextServerSeq:  1,
		inboundPending: make(map[uint64]protocol.Message),
		outbound:       session.NewPendingQueue(4096, 64*1024*1024),
		lastActive:     time.Now(),
		done:           make(chan struct{}),
	}
}

func (s *Session) Accept(msg protocol.Message) (uint64, error) {
	s.mu.Lock()
	s.lastActive = time.Now()
	if s.closed {
		ack := s.clientAck
		s.mu.Unlock()
		return ack, nil
	}
	if msg.Seq > s.clientAck {
		if _, ok := s.inboundPending[msg.Seq]; !ok {
			s.inboundPending[msg.Seq] = msg
		}
	}
	var ordered []protocol.Message
	for {
		next := s.clientAck + 1
		msg, ok := s.inboundPending[next]
		if !ok {
			break
		}
		delete(s.inboundPending, next)
		s.clientAck = next
		ordered = append(ordered, msg)
	}
	ack := s.clientAck
	s.mu.Unlock()

	for _, m := range ordered {
		if err := s.apply(m); err != nil {
			s.enqueueError(err.Error())
			s.Close()
			return ack, nil
		}
	}
	return ack, nil
}

func (s *Session) DownResponse(ack uint64, maxBatch int) protocol.DownResponse {
	s.touch()
	s.outbound.AckThrough(ack)

	chunks := s.outbound.Batch(maxBatch)
	if len(chunks) == 0 {
		select {
		case <-s.outbound.Notify():
		case <-s.done:
		case <-time.After(250 * time.Millisecond):
		}
		chunks = s.outbound.Batch(maxBatch)
	}

	return protocol.DownResponse{
		SID:    s.sid,
		Ack:    s.currentClientAck(),
		Chunks: chunks,
	}
}

func (s *Session) IdleFor(now time.Time) time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return now.Sub(s.lastActive)
}

func (s *Session) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		conn := s.upstream
		s.mu.Unlock()

		close(s.done)
		if conn != nil {
			_ = conn.Close()
		}
	})
}

func (s *Session) apply(msg protocol.Message) error {
	switch msg.Type {
	case protocol.TypeOpen:
		return s.open(msg)
	case protocol.TypeData:
		return s.writeData(msg)
	case protocol.TypeClose:
		s.Close()
		return nil
	case protocol.TypeError:
		s.logger.Warnf("client reported error sid=%s: %s", s.sid, msg.Message)
		s.Close()
		return nil
	default:
		return fmt.Errorf("unexpected message type %q", msg.Type)
	}
}

func (s *Session) open(msg protocol.Message) error {
	s.mu.Lock()
	if s.upstream != nil {
		s.mu.Unlock()
		return errors.New("session is already open")
	}
	s.mu.Unlock()

	addr, err := s.targetAddr(msg)
	if err != nil {
		return err
	}
	dialer := net.Dialer{Timeout: s.cfg.DialTimeout}
	conn, err := dialer.Dial(s.cfg.DialNetwork, addr)
	if err != nil {
		return fmt.Errorf("dial target failed: %w", err)
	}

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = conn.Close()
		return errors.New("session closed")
	}
	s.upstream = conn
	s.writeCh = make(chan []byte, 256)
	s.lastActive = time.Now()
	writeCh := s.writeCh
	s.mu.Unlock()

	s.logger.Infof("opened upstream sid=%s target=%s", s.sid, addr)
	go s.readTarget(conn)
	go s.writeTarget(conn, writeCh)
	return nil
}

func (s *Session) targetAddr(msg protocol.Message) (string, error) {
	if s.cfg.FixedUpstream != "" {
		host, port, err := net.SplitHostPort(s.cfg.FixedUpstream)
		if err != nil {
			return "", fmt.Errorf("invalid fixed upstream: %w", err)
		}
		if host == "" || port == "" {
			return "", errors.New("invalid fixed upstream")
		}
		return net.JoinHostPort(host, port), nil
	}
	if msg.TargetHost == "" || msg.TargetPort <= 0 || msg.TargetPort > 65535 {
		return "", errors.New("target host and port are required unless fixed upstream is configured")
	}
	return net.JoinHostPort(msg.TargetHost, strconv.Itoa(msg.TargetPort)), nil
}

func (s *Session) writeData(msg protocol.Message) error {
	data, err := protocol.DecodeBytes(msg.Data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	writeCh := s.writeCh
	closed := s.closed
	s.mu.Unlock()
	if closed || writeCh == nil {
		return errors.New("session is not open")
	}

	select {
	case writeCh <- data:
		s.touch()
		return nil
	case <-s.done:
		return errors.New("session closed")
	case <-time.After(s.cfg.DialTimeout):
		return errors.New("target write queue blocked")
	}
}

func (s *Session) readTarget(conn net.Conn) {
	buf := make([]byte, s.cfg.ChunkSize)
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			data := append([]byte(nil), buf[:n]...)
			if enqueueErr := s.enqueueData(data); enqueueErr != nil {
				s.logger.Warnf("downstream queue failed sid=%s: %v", s.sid, enqueueErr)
				s.enqueueError("downstream queue full")
				s.Close()
				return
			}
		}
		if err != nil {
			if !s.isClosed() {
				s.enqueueClose()
				s.Close()
			}
			return
		}
	}
}

func (s *Session) writeTarget(conn net.Conn, writeCh <-chan []byte) {
	for {
		select {
		case data := <-writeCh:
			if len(data) == 0 {
				continue
			}
			if _, err := conn.Write(data); err != nil {
				if !s.isClosed() {
					s.enqueueError("target write failed")
					s.Close()
				}
				return
			}
			s.touch()
		case <-s.done:
			return
		}
	}
}

func (s *Session) enqueueData(data []byte) error {
	return s.enqueue(protocol.TypeData, data, "")
}

func (s *Session) enqueueClose() {
	_ = s.enqueue(protocol.TypeClose, nil, "")
}

func (s *Session) enqueueError(message string) {
	_ = s.enqueue(protocol.TypeError, nil, message)
}

func (s *Session) enqueue(typ string, data []byte, text string) error {
	s.mu.Lock()
	seq := s.nextServerSeq
	s.nextServerSeq++
	s.lastActive = time.Now()
	s.mu.Unlock()

	msg := protocol.Message{
		SID:     s.sid,
		Seq:     seq,
		Type:    typ,
		Message: text,
	}
	if data != nil {
		msg.Data = protocol.EncodeBytes(data)
	}
	return s.outbound.Enqueue(msg)
}

func (s *Session) touch() {
	s.mu.Lock()
	s.lastActive = time.Now()
	s.mu.Unlock()
}

func (s *Session) currentClientAck() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clientAck
}

func (s *Session) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func parseUintQuery(r *http.Request, name string) (uint64, error) {
	value := r.URL.Query().Get(name)
	if value == "" {
		return 0, nil
	}
	return strconv.ParseUint(value, 10, 64)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
